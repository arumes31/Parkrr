//go:build integration

package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// buildTaxPaymentHistory reads its three linked tables from one read-only
// snapshot. The existing tax summary has separate reads; reject a changed
// payment set rather than silently joining its stale receipts to new history.
func (h *Handler) buildTaxPaymentHistory(ctx context.Context, rep taxYearReport) (taxPaymentHistory, error) {
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return taxPaymentHistory{}, fmt.Errorf("begin payment history snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	history, err := loadTaxPaymentHistory(ctx, tx, rep)
	if err != nil {
		return history, err
	}
	if err := tx.Commit(ctx); err != nil {
		return history, fmt.Errorf("finish payment history snapshot: %w", err)
	}
	return history, nil
}

func TestTaxPaymentHistoryManualReversal(t *testing.T) {
	for _, target := range []string{"charge", "invoice"} {
		t.Run(target, func(t *testing.T) {
			h := testHandler(t)
			h.Now = func() time.Time { return time.Date(2086, 6, 1, 12, 0, 0, 0, time.UTC) }
			pid := createIntegrationPerson(t, h)
			mkChargeP(t, h, pid, "Export history", 100, "2086-05-01")
			if target == "invoice" {
				compliantSeller(t, h)
				createInvoice(t, h, pid)
				payInvoices(t, h, pid, map[string]any{"amount": 100, "method": "bar", "auto": true, "paid_on": "2086-06-01"})
			} else {
				rec := postPayment(t, h, pid, map[string]any{
					"amount": 100, "method": "bar", "paid_on": "2086-06-01", "allocate": true, "note": "=1+1",
				})
				if rec.Code != http.StatusCreated {
					t.Fatalf("payment: %d %s", rec.Code, rec.Body.String())
				}
			}
			var id int64
			var paidOn time.Time
			if err := h.Pool.QueryRow(t.Context(), `SELECT id,paid_on FROM payments WHERE person_id=$1`, pid).Scan(&id, &paidOn); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodDelete, "/payment", nil)
			request.SetPathValue("id", strconv.FormatInt(id, 10))
			recorder := httptest.NewRecorder()
			h.DeletePayment(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("reverse: %d %s", recorder.Code, recorder.Body.String())
			}
			files := readTaxHistoryPackage(t, h, paidOn.Year())
			ledger := historyCSVRows(t, files["zahlungsverlauf.csv"])
			row := historyRowByID(t, ledger, "payment_id", id)
			if row["korrigiert"] != "ja" || row["in_einnahmen_csv"] != "nein" || row["betrag_eur"] != "100,00" {
				t.Fatalf("wrong documentary reversal: %v", row)
			}
			if row["ruecknahme_nachweis"] != "zuordnungsnachweis_vorhanden" {
				t.Fatalf("missing evidence: %v", row)
			}
			if target == "charge" && row["notiz"] != "'=1+1" {
				t.Fatalf("formula not guarded: %q", row["notiz"])
			}
			auditRows := historyCSVRows(t, files["zahlungsaudit.csv"])
			key := "released_allocations"
			if target == "invoice" {
				key = "released_invoice_payments"
			}
			found := false
			for _, entry := range auditRows {
				var changes map[string]json.RawMessage
				if err := json.Unmarshal([]byte(entry["aenderungen_json"]), &changes); err != nil {
					t.Fatal(err)
				}
				var released []struct {
					ID        int64   `json:"id"`
					PaymentID int64   `json:"payment_id"`
					Amount    float64 `json:"amount"`
				}
				if len(changes[key]) == 0 {
					continue
				}
				if err := json.Unmarshal(changes[key], &released); err != nil {
					t.Fatal(err)
				}
				for _, link := range released {
					if link.PaymentID == id && link.ID > 0 && link.Amount == 100 {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("released %s not traceable", key)
			}
			for _, row := range historyCSVRows(t, files["einnahmen.csv"]) {
				if row["payment_id"] == strconv.FormatInt(id, 10) {
					t.Fatal("reversal counted as income")
				}
			}
		})
	}
}

func TestTaxPaymentHistoryInvoiceCancellation(t *testing.T) {
	h := testHandler(t)
	compliantSeller(t, h)
	pid := createIntegrationPerson(t, h)
	chargeFor(t, h, pid, 100)
	invoice := createInvoice(t, h, pid)
	payInvoices(t, h, pid, map[string]any{"amount": 100, "method": "bar", "auto": true})
	var paymentID int64
	var paidOn time.Time
	if err := h.Pool.QueryRow(t.Context(), `SELECT id,paid_on FROM payments WHERE person_id=$1`, pid).Scan(&paymentID, &paidOn); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/invoice", nil)
	request.SetPathValue("id", strconv.FormatInt(invoice.ID, 10))
	recorder := httptest.NewRecorder()
	h.CancelInvoice(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel invoice: %d %s", recorder.Code, recorder.Body.String())
	}
	files := readTaxHistoryPackage(t, h, paidOn.Year())
	row := historyRowByID(t, historyCSVRows(t, files["zahlungsverlauf.csv"]), "payment_id", paymentID)
	if row["korrigiert"] != "nein" || row["in_einnahmen_csv"] != "ja" {
		t.Fatalf("invoice cancellation must not reverse money: %v", row)
	}
	found := false
	for _, row := range historyCSVRows(t, files["zahlungsaudit.csv"]) {
		if row["entitaet"] == "invoice" && row["entitaet_id"] == strconv.FormatInt(invoice.ID, 10) {
			if !strings.Contains(row["aenderungen_json"], "released_invoice_payments") || !strings.Contains(row["aenderungen_json"], "cancellation_invoice_id") {
				t.Fatalf("cancellation links missing: %v", row)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("invoice cancellation audit not linked to payment history")
	}
}

func TestTaxPaymentHistoryIntermediateYearsAndLegacyGap(t *testing.T) {
	h := testHandler(t)
	h.TaxWritesEnabled = true
	pid := createIntegrationPerson(t, h)
	cid := mkChargeP(t, h, pid, "Metadata history", 70, "2026-01-01")
	setChargePaidT(t, h, cid, true)
	var id, propertyID int64
	if err := h.Pool.QueryRow(t.Context(), `SELECT p.id,tp.id FROM payments p CROSS JOIN tax_properties tp WHERE p.person_id=$1 ORDER BY tp.id LIMIT 1`, pid).Scan(&id, &propertyID); err != nil {
		t.Fatal(err)
	}
	for _, year := range []int{2082, 2083, 2084} {
		body := fmtHistoryMetadata(t, year, propertyID)
		request := httptest.NewRequest(http.MethodPut, "/metadata", bytes.NewReader(body))
		request.SetPathValue("id", strconv.FormatInt(id, 10))
		recorder := httptest.NewRecorder()
		h.UpdatePaymentTaxMetadata(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("metadata: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	for _, year := range []int{2082, 2083, 2084} {
		files := readTaxHistoryPackage(t, h, year)
		row := historyRowByID(t, historyCSVRows(t, files["zahlungsverlauf.csv"]), "payment_id", id)
		wantIncluded := "nein"
		if year == 2084 {
			wantIncluded = "ja"
		}
		if row["in_einnahmen_csv"] != wantIncluded || row["zuflussdatum_aktuell"] != "2084-06-01" {
			t.Fatalf("year %d: %v", year, row)
		}
	}
	setChargePaidT(t, h, cid, false)
	files := readTaxHistoryPackage(t, h, 2083)
	row := historyRowByID(t, historyCSVRows(t, files["zahlungsverlauf.csv"]), "payment_id", id)
	if row["korrigiert"] != "ja" || row["ruecknahme_nachweis"] != "zuordnungsnachweis_vorhanden" {
		t.Fatalf("slider reversal missing: %v", row)
	}
	var legacyID int64
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO payments(person_id,amount,paid_on,reversed)
		VALUES($1,25,'2083-05-01',true) RETURNING id`, pid).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	files = readTaxHistoryPackage(t, h, 2083)
	row = historyRowByID(t, historyCSVRows(t, files["zahlungsverlauf.csv"]), "payment_id", legacyID)
	if row["ruecknahme_nachweis"] != "zuordnungsnachweis_fehlt" || row["korrigiert_am"] != "" {
		t.Fatalf("legacy evidence was invented: %v", row)
	}
}

func TestTaxPaymentHistoryRejectsChangedPaymentSet(t *testing.T) {
	for _, change := range []string{"added", "reversed", "date", "property"} {
		t.Run(change, func(t *testing.T) {
			h := testHandler(t)
			pid := createIntegrationPerson(t, h)
			pay := func() {
				t.Helper()
				rec := postPayment(t, h, pid, map[string]any{"amount": 10, "method": "bar", "paid_on": "2081-05-01"})
				if rec.Code != http.StatusCreated {
					t.Fatalf("payment: %d %s", rec.Code, rec.Body.String())
				}
			}
			pay()
			rep, err := h.buildTaxYearReport(t.Context(), 2081)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "added":
				pay()
			case "reversed":
				if _, err := h.Pool.Exec(t.Context(), `UPDATE payments SET reversed=true,reversed_at=now() WHERE person_id=$1`, pid); err != nil {
					t.Fatal(err)
				}
			case "date", "property":
				var paymentID, propertyID int64
				if err := h.Pool.QueryRow(t.Context(), `SELECT p.id,tp.id FROM payments p CROSS JOIN tax_properties tp
					WHERE p.person_id=$1 ORDER BY tp.is_default DESC,tp.id LIMIT 1`, pid).Scan(&paymentID, &propertyID); err != nil {
					t.Fatal(err)
				}
				receivedOn := "2081-05-02"
				if change == "property" {
					receivedOn = "2081-05-01"
					if err := h.Pool.QueryRow(t.Context(), `INSERT INTO tax_properties(name) VALUES('Integration history') RETURNING id`).Scan(&propertyID); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_, err := h.Pool.Exec(context.Background(), `DELETE FROM payment_tax_metadata WHERE payment_id=$1`, paymentID)
						if err != nil {
							t.Error(err)
						}
						_, err = h.Pool.Exec(context.Background(), `DELETE FROM tax_properties WHERE id=$1`, propertyID)
						if err != nil {
							t.Error(err)
						}
					})
				}
				if _, err := h.Pool.Exec(t.Context(), `INSERT INTO payment_tax_metadata(payment_id,received_on,tax_property_id)
					VALUES($1,$2::date,$3)`, paymentID, receivedOn, propertyID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.buildTaxPaymentHistory(t.Context(), rep); !errors.Is(err, errTaxExportChanged) {
				t.Fatalf("stale export was accepted: %v", err)
			}
		})
	}
}

func TestTaxPaymentHistorySingleConnectionAndLegacyAudit(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	var paymentID int64
	if err := h.Pool.QueryRow(t.Context(), `INSERT INTO payments(person_id,amount,paid_on,note)
		VALUES($1,10,'2080-05-01','Prüfung') RETURNING id`, pid).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	// Legacy JSON can have valid JSON syntax but unexpected field shapes.
	if _, err := h.Pool.Exec(t.Context(), `INSERT INTO audit_log(action,entity,entity_id,username,summary,changes)
		VALUES('update','payment_tax_metadata',$1,'=Prüfer','+Änderung',
		'{"received_on":{"old":"kein Datum","new":[]},"released_invoice_payments":{}}')`, paymentID); err != nil {
		t.Fatal(err)
	}
	cfg := h.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	single := New(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rep, err := single.buildTaxYearReport(ctx, 2080)
	if err != nil {
		t.Fatal(err)
	}
	history, err := single.buildTaxPaymentHistory(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	row := historyRowByID(t, historyCSVRows(t, history.Payments.Bytes()), "payment_id", paymentID)
	if row["notiz"] != "Prüfung" {
		t.Fatalf("Unicode lost: %v", row)
	}
	found := false
	for _, audit := range historyCSVRows(t, history.Audit.Bytes()) {
		if audit["entitaet_id"] == strconv.FormatInt(paymentID, 10) && audit["benutzer"] == "'=Prüfer" {
			found = true
			if audit["beschreibung"] != "'+Änderung" {
				t.Fatalf("unsafe summary: %v", audit)
			}
		}
	}
	if !found {
		t.Fatal("legacy audit missing or unsafe")
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := single.buildTaxPaymentHistory(canceled, rep); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func readTaxHistoryPackage(t *testing.T, h *Handler, year int) map[string][]byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.TaxYearPackage(recorder, httptest.NewRequest(http.MethodGet, "/api/reports/tax-year.zip?year="+strconv.Itoa(year), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("package: %d %s", recorder.Code, recorder.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range reader.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = data
	}
	var manifest struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil || manifest.Version != 3 {
		t.Fatalf("invalid manifest: %+v %v", manifest, err)
	}
	for _, name := range []string{"zahlungsverlauf.csv", "zahlungsaudit.csv", "zuordnungen.csv"} {
		if !bytes.HasPrefix(files[name], []byte{0xef, 0xbb, 0xbf}) {
			t.Fatalf("missing UTF-8 BOM: %s", name)
		}
	}
	return files
}

func historyCSVRows(t *testing.T, data []byte) []map[string]string {
	t.Helper()
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	reader.Comma = ';'
	rows, err := reader.ReadAll()
	if err != nil || len(rows) == 0 {
		t.Fatalf("invalid CSV: %v", err)
	}
	out := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		values := map[string]string{}
		for i, header := range rows[0] {
			values[header] = row[i]
		}
		out = append(out, values)
	}
	return out
}

func historyRowByID(t *testing.T, rows []map[string]string, key string, id int64) map[string]string {
	t.Helper()
	for _, row := range rows {
		if row[key] == strconv.FormatInt(id, 10) {
			return row
		}
	}
	t.Fatalf("missing %s=%d", key, id)
	return nil
}

func fmtHistoryMetadata(t *testing.T, year int, propertyID int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"received_on": strconv.Itoa(year) + "-06-01", "property_id": propertyID})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
