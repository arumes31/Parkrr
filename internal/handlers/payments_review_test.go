//go:build integration

package handlers

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestReviewCollectedVehicleRetainsUnbilledFinalPeriod(t *testing.T) {
	h := testHandler(t)
	compliantSeller(t, h)
	clock := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return clock }
	pid := createIntegrationPerson(t, h)
	vid := mkStoredVehicle(t, h, pid, 30, "2026-09-01")
	setStatus(t, h, vid, "collected", "2026-10-06")
	iv := createInvoice(t, h, pid)
	if math.Abs(iv.Total-30) > 0.005 {
		t.Fatalf("fixture: only completed September must be invoiced, got %.2f", iv.Total)
	}
	payInvoices(t, h, pid, map[string]any{"amount": iv.Total, "method": "bar", "auto": true})
	v := getVehicleT(t, h, pid, vid)
	if v.Archived {
		t.Errorf("final October 1-6 rent is not yet invoiced, but paying September archived vehicle %d", vid)
	}
	clock = time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	stats := personStatsT(t, h, pid)
	rec := issueInvoiceRec(t, h, pid)
	if rec.Code != http.StatusCreated {
		t.Errorf("completed final period must remain billable; balance=%.2f invoice status=%d body=%s", stats.Balance, rec.Code, rec.Body.String())
	}
}

func TestReviewLockedTaxYearRejectsBackdatedPaymentAndReversal(t *testing.T) {
	h := testHandler(t)
	ctx := context.Background()
	const year = 2097
	pid := createIntegrationPerson(t, h)
	if _, err := h.Pool.Exec(ctx, `DELETE FROM tax_year_locks WHERE year=$1`, year); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := h.Pool.Exec(ctx, `DELETE FROM tax_year_locks WHERE year=$1`, year); err != nil {
			t.Errorf("cleanup test year lock: %v", err)
		}
	})
	prior := postPayment(t, h, pid, map[string]any{"amount": 100, "paid_on": "2097-06-01", "method": "ueberweisung"})
	if prior.Code != http.StatusCreated {
		t.Fatalf("fixture payment: %d %s", prior.Code, prior.Body.String())
	}
	var p payment
	if err := json.Unmarshal(prior.Body.Bytes(), &p); err != nil || p.ID == 0 {
		t.Fatalf("fixture payment decode: %v, %s", err, prior.Body.String())
	}
	lockReq := httptest.NewRequest(http.MethodPost, "/api/tax/years/2097/lock", nil)
	lockReq.SetPathValue("year", "2097")
	lockRec := httptest.NewRecorder()
	h.LockTaxYear(lockRec, lockReq)
	if lockRec.Code != http.StatusOK {
		t.Fatalf("fixture lock: %d %s", lockRec.Code, lockRec.Body.String())
	}
	before, err := h.buildTaxYearReport(ctx, year)
	if err != nil {
		t.Fatal(err)
	}
	created := postPayment(t, h, pid, map[string]any{"amount": 25, "paid_on": "2097-06-02", "method": "bar"})
	if created.Code != http.StatusConflict {
		t.Errorf("backdated payment into locked tax year must return 409, got %d %s", created.Code, created.Body.String())
	}
	reverseReq := httptest.NewRequest(http.MethodDelete, "/api/payments/"+strconv.FormatInt(p.ID, 10), nil)
	reverseReq.SetPathValue("id", strconv.FormatInt(p.ID, 10))
	reverseRec := httptest.NewRecorder()
	h.DeletePayment(reverseRec, reverseReq)
	if reverseRec.Code != http.StatusConflict {
		t.Errorf("reversal affecting locked tax year must return 409, got %d %s", reverseRec.Code, reverseRec.Body.String())
	}
	after, err := h.buildTaxYearReport(ctx, year)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Locked || math.Abs(before.TotalByDate-after.TotalByDate) > 0.005 {
		t.Errorf("locked report changed: locked=%v before=%.2f after=%.2f", after.Locked, before.TotalByDate, after.TotalByDate)
	}
}

func TestReviewExistingAdvanceCanSettleInvoiceWithoutNewMoney(t *testing.T) {
	h := testHandler(t)
	compliantSeller(t, h)
	h.Now = func() time.Time { return time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC) }
	pid := createIntegrationPerson(t, h)
	advance := postPayment(t, h, pid, map[string]any{"amount": 100, "paid_on": "2026-05-01", "method": "ueberweisung"})
	if advance.Code != http.StatusCreated {
		t.Fatalf("advance: %d %s", advance.Code, advance.Body.String())
	}
	chargeFor(t, h, pid, 100)
	iv := createInvoice(t, h, pid)
	before := personStatsT(t, h, pid)
	if math.Abs(before.Balance) > 0.005 {
		t.Fatalf("fixture: incoming cash exactly covers charge, balance=%.2f", before.Balance)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/persons/"+strconv.FormatInt(pid, 10)+"/apply-credit", nil)
	req.SetPathValue("id", strconv.FormatInt(pid, 10))
	rec := httptest.NewRecorder()
	h.ApplyCredit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply-credit: %d %s", rec.Code, rec.Body.String())
	}
	current := getInvoiceT(t, h, iv.ID)
	if current.OpenAmount > 0.005 {
		t.Errorf("existing EUR 100 receipt must settle EUR 100 invoice without new receipt; balance=%.2f open=%.2f apply=%s", before.Balance, current.OpenAmount, rec.Body.String())
	}
	var count int
	var total float64
	if err := h.Pool.QueryRow(context.Background(), `SELECT count(*), COALESCE(sum(amount),0) FROM payments WHERE person_id=$1 AND NOT reversed`, pid).Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	if count != 1 || math.Abs(total-100) > 0.005 {
		t.Errorf("applying advance must preserve one EUR 100 receipt; count=%d sum=%.2f", count, total)
	}
}

func TestReviewCollectionRejectsEndBeforeStart(t *testing.T) {
	h := testHandler(t)
	h.Now = func() time.Time { return time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC) }
	pid := createIntegrationPerson(t, h)
	vid := mkStoredVehicle(t, h, pid, 30, "2026-10-06")
	req := taxJSONRequest(t, http.MethodPost, "/api/vehicles/"+strconv.FormatInt(vid, 10)+"/status",
		map[string]any{"status": "collected", "date": "2026-10-01"})
	req.SetPathValue("id", strconv.FormatInt(vid, 10))
	rec := httptest.NewRecorder()
	h.ChangeVehicleStatus(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("collection before storage start must return 400; got %d %s", rec.Code, rec.Body.String())
	}
	v := getVehicleT(t, h, pid, vid)
	if v.EndDate != nil && v.EndDate.Before(v.StartDate) {
		t.Errorf("invalid interval persisted: start=%s end=%s", v.StartDate.Format(dateLayout), v.EndDate.Format(dateLayout))
	}
}
