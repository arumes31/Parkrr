package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// InvoicePDF returns a real PDF document for an existing invoice and 404 for an
// unknown id. Runs only when PARKRR_TEST_DATABASE_URL is set.
func TestInvoicePDF(t *testing.T) {
	h := testHandler(t)
	compliantSeller(t, h)
	pid := createIntegrationPerson(t, h)
	chargeFor(t, h, pid, 12.5)
	iv := createInvoice(t, h, pid)

	req := httptest.NewRequest(http.MethodGet, "/api/invoices/"+strconv.FormatInt(iv.ID, 10)+"/pdf", nil)
	req.SetPathValue("id", strconv.FormatInt(iv.ID, 10))
	w := httptest.NewRecorder()
	h.InvoicePDF(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("pdf: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition")
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Errorf("body is not a PDF (prefix %q)", body[:min(8, len(body))])
	}
	if len(body) < 800 {
		t.Errorf("PDF suspiciously small: %d bytes", len(body))
	}

	// Unknown id → 404.
	nreq := httptest.NewRequest(http.MethodGet, "/api/invoices/999999999/pdf", nil)
	nreq.SetPathValue("id", "999999999")
	nw := httptest.NewRecorder()
	h.InvoicePDF(nw, nreq)
	if nw.Code != http.StatusNotFound {
		t.Errorf("unknown invoice: got %d, want 404", nw.Code)
	}
}

func TestInvoicePDFPaymentQRRespectsBalance(t *testing.T) {
	for _, tc := range []struct {
		name             string
		paid             float64
		canceled, wantQR bool
	}{
		{"open", 0, false, true}, {"partial", 40, false, true},
		{"paid", 100, false, false}, {"canceled", 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iv := invoice{Number: "DEMO-QR", Total: 100, PaidAmount: tc.paid, OpenAmount: 100 - tc.paid, Canceled: tc.canceled,
				Seller: map[string]any{"name": "Synthetic Demo", "iban": "AT611904300234573201"}}
			rec := httptest.NewRecorder()
			writeInvoicePDF(rec, iv)
			if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
				t.Fatal("invalid PDF")
			}
			if got := bytes.Contains(rec.Body.Bytes(), []byte("/Subtype /Image")); got != tc.wantQR {
				t.Fatalf("payment image: got %v want %v", got, tc.wantQR)
			}
		})
	}
}
