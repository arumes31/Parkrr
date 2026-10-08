package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTaxMutationsRequireActivation calls handlers directly, without a router or
// database: neither an omitted middleware nor an upload may bypass the guard.
func TestTaxMutationsRequireActivation(t *testing.T) {
	t.Parallel()
	mutations := []struct {
		name    string
		handler func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{name: "create property", handler: (*Handler).CreateTaxProperty},
		{name: "update property", handler: (*Handler).UpdateTaxProperty},
		{name: "save settings", handler: (*Handler).SaveTaxSettings},
		{name: "create expense", handler: (*Handler).CreateTaxExpense},
		{name: "reverse expense", handler: (*Handler).ReverseTaxExpense},
		{name: "upload receipt", handler: (*Handler).UploadTaxReceipt},
		{name: "create recurring", handler: (*Handler).CreateTaxRecurring},
		{name: "update recurring", handler: (*Handler).UpdateTaxRecurring},
		{name: "book recurring", handler: (*Handler).BookTaxRecurring},
		{name: "create asset", handler: (*Handler).CreateTaxAsset},
		{name: "update payment metadata", handler: (*Handler).UpdatePaymentTaxMetadata},
		{name: "lock year", handler: (*Handler).LockTaxYear},
		{name: "unlock year", handler: (*Handler).UnlockTaxYear},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, state := range []struct {
				name    string
				handler *Handler
			}{
				{name: "nil handler"},
				{name: "zero handler", handler: &Handler{}},
				{name: "constructed handler", handler: New(nil)},
			} {
				t.Run(state.name, func(t *testing.T) {
					body := &taxUnreadBody{t: t}
					req := httptest.NewRequest(http.MethodPost, "/tax-mutation", nil)
					req.Body = body
					req.SetPathValue("id", "1")
					req.SetPathValue("year", "2025")
					rec := httptest.NewRecorder()
					tc.handler(state.handler, rec, req)
					if rec.Code != http.StatusForbidden {
						t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
					}
					var response struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(response.Error, "nur lesend") {
						t.Fatalf("missing read-only explanation: %s", rec.Body.String())
					}
					if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
						t.Fatal("expected JSON error response")
					}
				})
			}

			// Invalid identifiers/body stop at existing validation, proving an
			// explicitly activated handler is not still blocked by the new guard.
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/tax-mutation", strings.NewReader("{"))
			tc.handler(&Handler{TaxWritesEnabled: true}, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("activated validation status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

type taxUnreadBody struct{ t *testing.T }

func (b *taxUnreadBody) Read([]byte) (int, error) {
	b.t.Error("read-only mutation read the request body")
	return 0, io.EOF
}

func (*taxUnreadBody) Close() error { return nil }

func TestTaxSettingsCannotActivateWrites(t *testing.T) {
	t.Parallel()
	h := New(nil)
	req := httptest.NewRequest(http.MethodPut, "/api/tax/settings", strings.NewReader(
		`{"small_business_limit":55000,"warning_percent":90,"write_enabled":true,"tax_writes_enabled":true}`))
	rec := httptest.NewRecorder()
	h.SaveTaxSettings(rec, req)
	if rec.Code != http.StatusForbidden || h.TaxWritesEnabled {
		t.Fatalf("settings changed activation: status=%d enabled=%v", rec.Code, h.TaxWritesEnabled)
	}
}

func TestTaxYearReportIncludesWriteCapability(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		enabled  bool
		expected string
	}{
		{name: "read only", expected: `"write_enabled":false`},
		{name: "activated", enabled: true, expected: `"write_enabled":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(taxYearReport{WriteEnabled: tc.enabled})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), tc.expected) {
				t.Fatalf("missing explicit capability %s in report", tc.expected)
			}
		})
	}
}
