package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func periodHistoryRequest(t *testing.T, handler http.HandlerFunc, id int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/period-history-test", bytes.NewReader(data))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func periodHistorySnapshot(t *testing.T, h *Handler, kind string, id, pid int64) string {
	t.Helper()
	var snapshot string
	if err := h.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'agreement',(SELECT to_jsonb(a) FROM flat_rate_periods a WHERE $1='agreement' AND id=$2),
		'recurring',(SELECT to_jsonb(r) FROM recurring_charges r WHERE $1='recurring' AND id=$2),
		'flags',(SELECT jsonb_agg(to_jsonb(f) ORDER BY period_key) FROM flat_rate_period_payments f WHERE $1='agreement' AND period_id=$2),
		'payments',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payments p WHERE person_id=$3),
		'metadata',(SELECT jsonb_agg(to_jsonb(m) ORDER BY payment_id) FROM payment_tax_metadata m
		   JOIN payments p ON p.id=m.payment_id WHERE p.person_id=$3),
		'vehicles',(SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM vehicles v WHERE person_id=$3),
		'audits',(SELECT count(*) FROM audit_log))::text`, kind, id, pid).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPeriodHistoryProtectsLegacySourceDeletion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     string
		reversed bool
		auto     bool
	}{
		{name: "legacy agreement", kind: "agreement", auto: true},
		{name: "reversed agreement", kind: "agreement", auto: true, reversed: true},
		{name: "reversed recurring", kind: "recurring", auto: true, reversed: true},
		{name: "restored linked nonauto", kind: "recurring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler(t)
			pid := createIntegrationPerson(t, h)
			start := firstOfMonthMonthsAgo(3)
			id := mkRecurring(t, h, pid, 30, start.Format(dateLayout))
			deleteHandler, periodHandler := h.DeleteRecurringCharge, h.SetRecurringChargePeriodPaid
			if tc.kind == "agreement" {
				id = mkAgreement(t, h, pid, 30, "monthly", start.Format(dateLayout))
				deleteHandler, periodHandler = h.DeleteAgreement, h.SetAgreementPeriodPaid
			}
			if _, err := h.Pool.Exec(t.Context(), `INSERT INTO payments
				(person_id,amount,method,auto,settles_kind,settles_ref,settles_period,reversed)
				VALUES($1,10,'bar',$2,$3,$4,$5,$6)`,
				pid, tc.auto, tc.kind, id, start.Format("2006-01"), tc.reversed); err != nil {
				t.Fatal(err)
			}
			for _, action := range []struct {
				name    string
				handler http.HandlerFunc
				body    any
			}{
				{name: "delete source", handler: deleteHandler},
				{name: "replace receipt", handler: periodHandler,
					body: map[string]any{"period_key": start.Format("2006-01"), "paid": true, "amount": 20}},
			} {
				t.Run(action.name, func(t *testing.T) {
					before := periodHistorySnapshot(t, h, tc.kind, id, pid)
					rec := periodHistoryRequest(t, action.handler, id, action.body)
					if rec.Code != http.StatusConflict {
						t.Fatalf("history guard: %d %s", rec.Code, rec.Body.String())
					}
					if after := periodHistorySnapshot(t, h, tc.kind, id, pid); after != before {
						t.Fatal("legacy source/payment changed")
					}
				})
			}
		})
	}
}

func TestPeriodHistoryRejectsDestructiveSliderActions(t *testing.T) {
	for _, kind := range []string{"agreement", "recurring"} {
		t.Run(kind, func(t *testing.T) {
			h := testHandler(t)
			pid := createIntegrationPerson(t, h)
			start := firstOfMonthMonthsAgo(3)
			key := start.Format("2006-01")
			id := mkRecurring(t, h, pid, 30, start.Format(dateLayout))
			periodHandler, masterHandler, deleteHandler := h.SetRecurringChargePeriodPaid, h.SetRecurringChargePaid, h.DeleteRecurringCharge
			if kind == "agreement" {
				id = mkAgreement(t, h, pid, 30, "monthly", start.Format(dateLayout))
				periodHandler, masterHandler, deleteHandler = h.SetAgreementPeriodPaid, h.SetAgreementPaid, h.DeleteAgreement
			}
			partial := map[string]any{"period_key": key, "paid": true, "amount": 10}
			if rec := periodHistoryRequest(t, periodHandler, id, partial); rec.Code != http.StatusOK {
				t.Fatalf("initial payment: %d %s", rec.Code, rec.Body.String())
			}
			// Real tax metadata must survive every rejected reset, not just the sum.
			if _, err := h.Pool.Exec(t.Context(), `INSERT INTO payment_tax_metadata(payment_id,received_on,tax_property_id)
				SELECT p.id,p.paid_on,t.id FROM payments p CROSS JOIN LATERAL
				(SELECT id FROM tax_properties ORDER BY id LIMIT 1) t
				WHERE p.settles_kind=$1 AND p.settles_ref=$2`, kind, id); err != nil {
				t.Fatal(err)
			}
			for _, action := range []struct {
				name    string
				handler http.HandlerFunc
				body    any
			}{
				{name: "replace partial", handler: periodHandler, body: map[string]any{"period_key": key, "paid": true, "amount": 20}},
				{name: "replace with whole", handler: periodHandler, body: map[string]any{"period_key": key, "paid": true}},
				{name: "reopen period", handler: periodHandler, body: map[string]any{"period_key": key, "paid": false}},
				{name: "reset master", handler: masterHandler, body: map[string]any{"paid": false}},
				{name: "replace through master", handler: masterHandler, body: map[string]any{"paid": true}},
				{name: "delete source", handler: deleteHandler},
			} {
				t.Run(action.name, func(t *testing.T) {
					before := periodHistorySnapshot(t, h, kind, id, pid)
					rec := periodHistoryRequest(t, action.handler, id, action.body)
					if rec.Code != http.StatusConflict {
						t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
					}
					if after := periodHistorySnapshot(t, h, kind, id, pid); after != before {
						t.Fatalf("rejected operation changed receipt, source, flags, metadata or audit\nbefore=%s\nafter=%s", before, after)
					}
				})
			}
			// Existing amount remains an idempotent receipt, while ordinary new
			// money is intentionally not affected by the period-slider restriction.
			var paymentBefore string
			if err := h.Pool.QueryRow(t.Context(), `SELECT to_jsonb(p)::text FROM payments p
				WHERE settles_kind=$1 AND settles_ref=$2 AND settles_period=$3`, kind, id, key).Scan(&paymentBefore); err != nil {
				t.Fatal(err)
			}
			if rec := periodHistoryRequest(t, periodHandler, id, partial); rec.Code != http.StatusOK {
				t.Fatalf("same amount retry: %d %s", rec.Code, rec.Body.String())
			}
			var paymentAfter string
			if err := h.Pool.QueryRow(t.Context(), `SELECT to_jsonb(p)::text FROM payments p
				WHERE settles_kind=$1 AND settles_ref=$2 AND settles_period=$3`, kind, id, key).Scan(&paymentAfter); err != nil {
				t.Fatal(err)
			}
			if paymentBefore != paymentAfter {
				t.Fatal("identical retry replaced receipt identity/date/metadata")
			}
			if rec := postPayment(t, h, pid, map[string]any{"amount": 5, "method": "bar"}); rec.Code != http.StatusCreated {
				t.Fatalf("ordinary new payment was blocked: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPeriodHistoryIncludesReversedReceiptsAndRetainsYearLocks(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	start := firstOfMonthMonthsAgo(3)
	id := mkRecurring(t, h, pid, 30, start.Format(dateLayout))
	key := start.Format("2006-01")
	if _, err := h.Pool.Exec(t.Context(), `INSERT INTO payments
		(person_id,amount,method,paid_on,auto,settles_kind,settles_ref,settles_period,reversed)
		VALUES($1,10,'bar','2096-06-01',true,'recurring',$2,$3,true)`, pid, id, key); err != nil {
		t.Fatal(err)
	}
	for _, paid := range []bool{true, false} {
		before := periodHistorySnapshot(t, h, "recurring", id, pid)
		rec := periodHistoryRequest(t, h.SetRecurringChargePeriodPaid, id,
			map[string]any{"period_key": key, "paid": paid, "amount": 10})
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Zahlungshistorie") {
			t.Fatalf("reversed receipt not protected: %d %s", rec.Code, rec.Body.String())
		}
		if after := periodHistorySnapshot(t, h, "recurring", id, pid); after != before {
			t.Fatal("reversed receipt or source changed")
		}
	}
	if _, err := h.Pool.Exec(t.Context(), `INSERT INTO tax_year_locks(year) VALUES(2096)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := h.Pool.Exec(context.Background(), `DELETE FROM tax_year_locks WHERE year=2096`); err != nil {
			t.Error(err)
		}
	})
	rec := periodHistoryRequest(t, h.SetRecurringChargePaid, id, map[string]any{"paid": false})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Steuerjahr 2096") {
		t.Fatalf("existing year lock lost priority: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPeriodHistoryAllowsFlagOnlyZeroPeriods(t *testing.T) {
	for _, kind := range []string{"agreement", "recurring"} {
		t.Run(kind, func(t *testing.T) {
			h := testHandler(t)
			pid := createIntegrationPerson(t, h)
			start := firstOfMonthMonthsAgo(3)
			id := mkRecurring(t, h, pid, 0, start.Format(dateLayout))
			handler := h.SetRecurringChargePeriodPaid
			if kind == "agreement" {
				// New agreements require positive amounts. An old zero-price
				// record still needs to support a flag-only reset without receipts.
				id = mkAgreement(t, h, pid, 1, "monthly", start.Format(dateLayout))
				if _, err := h.Pool.Exec(t.Context(), `UPDATE flat_rate_periods SET amount=0 WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				handler = h.SetAgreementPeriodPaid
			}
			for _, paid := range []bool{true, false} {
				rec := periodHistoryRequest(t, handler, id,
					map[string]any{"period_key": start.Format("2006-01"), "paid": paid})
				if rec.Code != http.StatusOK {
					t.Fatalf("zero period paid=%v: %d %s", paid, rec.Code, rec.Body.String())
				}
			}
			if sum, count := settlesPaymentSum(t, h, pid); count != 0 || sum != 0 {
				t.Fatalf("zero period created receipt: count=%d amount=%v", count, sum)
			}
		})
	}
}
