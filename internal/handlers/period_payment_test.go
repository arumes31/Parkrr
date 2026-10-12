package handlers

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// setAgreementPeriodPaidT toggles one sub-period of an agreement paid/open.
func setAgreementPeriodPaidT(t *testing.T, h *Handler, aid int64, key string, paid bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"period_key": key, "paid": paid})
	req := httptest.NewRequest(http.MethodPost, "/api/agreements/"+strconv.FormatInt(aid, 10)+"/period-paid", bytes.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(aid, 10))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.SetAgreementPeriodPaid(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("period-paid(%v): %d %s", paid, rec.Code, rec.Body.String())
	}
}

// settlesPaymentSum returns the person's real period-settlement money-in and count.
func settlesPaymentSum(t *testing.T, h *Handler, pid int64) (float64, int) {
	t.Helper()
	var sum float64
	var n int
	if err := h.Pool.QueryRow(t.Context(),
		`SELECT COALESCE(SUM(amount),0), COUNT(*) FROM payments
		   WHERE person_id=$1 AND settles_kind IS NOT NULL AND NOT reversed`, pid).Scan(&sum, &n); err != nil {
		t.Fatalf("settles sum: %v", err)
	}
	return sum, n
}

// TestPauschalePeriodPaidRecordsPayment (Fix 1): marking a Pauschale sub-period
// paid books a REAL Zahlungseingang for it — visible in payments and PaymentsTotal
// — while the balance moves by exactly the period cost (no double-count with the
// off-book credit). A reset cannot erase the resulting payment history.
func TestPauschalePeriodPaidRecordsPayment(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	aid := mkAgreement(t, h, pid, 30, "monthly", firstOfMonthMonthsAgo(3).Format("2006-01-02"))
	key := firstOfMonthMonthsAgo(3).Format("2006-01") // a completed month

	before := personStatsT(t, h, pid)
	setAgreementPeriodPaidT(t, h, aid, key, true)
	after := personStatsT(t, h, pid)

	// Balance dropped by exactly the 30€ period (no phantom, no double-count).
	if diff := before.Balance - after.Balance; math.Abs(diff-30) > 0.05 {
		t.Errorf("balance must drop by 30, got %.2f (before %.2f after %.2f)", diff, before.Balance, after.Balance)
	}
	// A real Zahlungseingang now exists and counts in PaymentsTotal.
	if sum, n := settlesPaymentSum(t, h, pid); n != 1 || math.Abs(sum-30) > 0.005 {
		t.Errorf("expected one 30€ period payment, got n=%d sum=%.2f", n, sum)
	}
	if math.Abs(after.PaymentsTotal-30) > 0.005 {
		t.Errorf("PaymentsTotal must include the period payment (30), got %.2f", after.PaymentsTotal)
	}

	// R03 interim guard: a reset must not remove this real receipt.
	rec := periodHistoryRequest(t, h.SetAgreementPeriodPaid, aid, map[string]any{"period_key": key, "paid": false})
	if rec.Code != http.StatusConflict {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	back := personStatsT(t, h, pid)
	if math.Abs(back.Balance-after.Balance) > 0.05 {
		t.Errorf("rejected reset changed balance: paid=%.2f after=%.2f", after.Balance, back.Balance)
	}
	if sum, n := settlesPaymentSum(t, h, pid); n != 1 || sum != 30 {
		t.Errorf("rejected reset lost the period payment: n=%d sum=%.2f", n, sum)
	}
}

// TestPeriodPaymentBackfill: an off-book settlement made before migration 036 (a
// flat_rate_period_payments row with no payment) is turned into a real
// Zahlungseingang by the idempotent backfill, without changing the balance.
func TestPeriodPaymentBackfill(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	aid := mkAgreement(t, h, pid, 30, "monthly", firstOfMonthMonthsAgo(3).Format("2006-01-02"))
	key := firstOfMonthMonthsAgo(3).Format("2006-01")

	// Simulate the pre-036 world: settle the period off-book only (no payment row).
	if _, err := h.Pool.Exec(t.Context(),
		`INSERT INTO flat_rate_period_payments (period_id, period_key) VALUES ($1,$2)`, aid, key); err != nil {
		t.Fatalf("seed off-book settlement: %v", err)
	}
	pre := personStatsT(t, h, pid)
	if sum, _ := settlesPaymentSum(t, h, pid); sum != 0 {
		t.Fatalf("precondition: no period payment yet, got %.2f", sum)
	}

	if err := h.BackfillPeriodPayments(t.Context()); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	post := personStatsT(t, h, pid)
	if sum, n := settlesPaymentSum(t, h, pid); n != 1 || math.Abs(sum-30) > 0.005 {
		t.Errorf("backfill must book one 30€ payment, got n=%d sum=%.2f", n, sum)
	}
	if math.Abs(post.Balance-pre.Balance) > 0.005 {
		t.Errorf("backfill must not change the balance: pre=%.2f post=%.2f", pre.Balance, post.Balance)
	}

	// Idempotent: a second run books nothing more.
	if err := h.BackfillPeriodPayments(t.Context()); err != nil {
		t.Fatalf("backfill (2nd): %v", err)
	}
	if _, n := settlesPaymentSum(t, h, pid); n != 1 {
		t.Errorf("backfill must be idempotent, got n=%d after second run", n)
	}
}

// TestRecurringPeriodPaidRecordsPayment (Fix 1, recurring path): the same for a
// vehicle-independent Nebenkosten period.
func TestRecurringPeriodPaidRecordsPayment(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)

	amt := 20.0
	rcBody, _ := json.Marshal(recurringRequest{
		Description: "Versicherung", Amount: &amt, Period: "monthly",
		StartDate: firstOfMonthMonthsAgo(3).Format("2006-01-02"),
	})
	rrec := httptest.NewRecorder()
	rreq := httptest.NewRequest(http.MethodPost, "/api/persons/"+strconv.FormatInt(pid, 10)+"/recurring", bytes.NewReader(rcBody))
	rreq.SetPathValue("id", strconv.FormatInt(pid, 10))
	rreq.Header.Set("Content-Type", "application/json")
	h.CreateRecurringCharge(rrec, rreq)
	if rrec.Code != http.StatusOK && rrec.Code != http.StatusCreated {
		t.Fatalf("recurring: %d %s", rrec.Code, rrec.Body.String())
	}
	var rc struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rrec.Body.Bytes(), &rc)
	if rc.ID == 0 { // list response — take newest
		var list []struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(rrec.Body.Bytes(), &list)
		for _, x := range list {
			if x.ID > rc.ID {
				rc.ID = x.ID
			}
		}
	}

	key := firstOfMonthMonthsAgo(3).Format("2006-01")
	before := personStatsT(t, h, pid)
	body, _ := json.Marshal(map[string]any{"period_key": key, "paid": true})
	req := httptest.NewRequest(http.MethodPost, "/api/recurring/"+strconv.FormatInt(rc.ID, 10)+"/period-paid", bytes.NewReader(body))
	req.SetPathValue("id", strconv.FormatInt(rc.ID, 10))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.SetRecurringChargePeriodPaid(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recurring period-paid: %d %s", rec.Code, rec.Body.String())
	}
	after := personStatsT(t, h, pid)

	if diff := before.Balance - after.Balance; math.Abs(diff-20) > 0.05 {
		t.Errorf("balance must drop by 20, got %.2f", diff)
	}
	if sum, n := settlesPaymentSum(t, h, pid); n != 1 || math.Abs(sum-20) > 0.005 {
		t.Errorf("expected one 20€ recurring period payment, got n=%d sum=%.2f", n, sum)
	}
}
