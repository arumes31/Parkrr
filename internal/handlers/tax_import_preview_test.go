package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const validTaxImportJSON = `{"version":1,"year":2025,"property_id":1,` +
	`"source":" external file ","reviewer":" reviewer ","entries":[` +
	`{"external_id":"ext:1","kind":"income_adjustment","amount_eur":"0012.3","description":" correction "}]}`

func taxImportJSON(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	req := map[string]any{}
	if err := json.Unmarshal([]byte(validTaxImportJSON), &req); err != nil {
		t.Fatal(err)
	}
	mutate(req)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func recordTaxImportPreview(body string) *httptest.ResponseRecorder {
	var h *Handler // A nil receiver fails immediately if the preview touches DB/config/state.
	req := httptest.NewRequest(http.MethodPost, "/api/reports/tax-import/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.TaxImportPreview(rec, req)
	return rec
}

func TestTaxImportPreviewStatelessReplay(t *testing.T) {
	t.Parallel()
	first := recordTaxImportPreview(validTaxImportJSON)
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", first.Code, first.Body.String())
	}
	var rep taxImportResponse
	if err := json.Unmarshal(first.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Preview || rep.Persisted || rep.Verified {
		t.Fatalf("not an unverified stateless preview: %+v", rep)
	}
	if rep.Version != 1 || rep.Year != 2025 || rep.PropertyID != 1 {
		t.Fatalf("wrong scope: %+v", rep)
	}
	if rep.Source != "external file" || rep.Reviewer != "reviewer" {
		t.Fatalf("provenance not normalized: %+v", rep)
	}
	wantEntry := taxImportEntry{
		ExternalID: "ext:1", Kind: "income_adjustment",
		AmountEUR: "12.30", AmountCents: 1230, Description: "correction",
	}
	if len(rep.Entries) != 1 || rep.Entries[0] != wantEntry {
		t.Fatalf("entries=%+v", rep.Entries)
	}
	if rep.TotalsCents != (taxImportTotals{IncomeAdjustment: 1230}) || len(rep.Notes) < 4 {
		t.Fatalf("missing totals or warnings: %+v", rep)
	}
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("financial preview must not be cached")
	}
	// Replays do not consume IDs or count as imports; every request is independent.
	for range 3 {
		next := recordTaxImportPreview(validTaxImportJSON)
		if next.Code != first.Code || next.Body.String() != first.Body.String() {
			t.Fatalf("replay changed response: %d %s", next.Code, next.Body.String())
		}
	}
	changed := strings.Replace(validTaxImportJSON, "0012.3", "7.89", 1)
	if rec := recordTaxImportPreview(changed); rec.Code != http.StatusOK {
		t.Fatalf("same external id in a different request rejected: %s", rec.Body.String())
	}
	if rec := recordTaxImportPreview(validTaxImportJSON); rec.Body.String() != first.Body.String() {
		t.Fatal("a subsequent preview changed prior values")
	}
}

func TestTaxImportPreviewRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "empty", body: ""},
		{name: "null", body: "null"},
		{name: "array", body: "[]"},
		{name: "extra document", body: validTaxImportJSON + `{}`},
		{name: "duplicate key", body: strings.Replace(validTaxImportJSON, `"version":1`, `"version":1,"version":1`, 1)},
		{
			name: "escaped duplicate key",
			body: strings.Replace(
				validTaxImportJSON,
				`"version":1`,
				`"version":1,"\u0076ersion":1`,
				1,
			),
		},
		{name: "case alias", body: strings.Replace(validTaxImportJSON, `"version"`, `"Version"`, 1)},
		{name: "unknown null field", body: strings.Replace(validTaxImportJSON, `"version":1`, `"version":1,"apply":null`, 1)},
		{
			name: "nested duplicate",
			body: strings.Replace(
				validTaxImportJSON,
				`"amount_eur":"0012.3"`,
				`"amount_eur":"1","amount_eur":"2"`,
				1,
			),
		},
		{
			name: "nested unknown null",
			body: strings.Replace(
				validTaxImportJSON,
				`"amount_eur":"0012.3"`,
				`"amount_eur":"1","verified":null`,
				1,
			),
		},
		{name: "amount number", body: strings.Replace(validTaxImportJSON, `"0012.3"`, `12.3`, 1)},
		{name: "id exponent", body: strings.Replace(validTaxImportJSON, `"property_id":1`, `"property_id":1e0`, 1)},
		{name: "id fractional", body: strings.Replace(validTaxImportJSON, `"property_id":1`, `"property_id":1.0`, 1)},
		{name: "invalid utf8", body: strings.Replace(validTaxImportJSON, "correction", string([]byte{0xff}), 1)},
		{name: "nested too deep", body: `{"entries":[[[[[[[[[[[]]]]]]]]]]]}`},
		{
			name: "oversize", body: validTaxImportJSON + strings.Repeat(" ", maxTaxImportBody),
			want: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := recordTaxImportPreview(tt.body)
			want := tt.want
			if want == 0 {
				want = http.StatusBadRequest
			}
			if rec.Code != want {
				t.Fatalf("status=%d want=%d: %s", rec.Code, want, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("invalid preview must not be cached")
			}
		})
	}
}

func TestTaxImportPreviewRequiredFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"version", "year", "property_id", "source", "reviewer", "entries"} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprintf("root %s null=%t", field, null), func(t *testing.T) {
				body := taxImportJSON(t, func(req map[string]any) {
					delete(req, field)
					if null {
						req[field] = nil
					}
				})
				if rec := recordTaxImportPreview(body); rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
	for _, field := range []string{"external_id", "kind", "amount_eur", "description"} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprintf("entry %s null=%t", field, null), func(t *testing.T) {
				body := taxImportJSON(t, func(req map[string]any) {
					entry := req["entries"].([]any)[0].(map[string]any)
					delete(entry, field)
					if null {
						entry[field] = nil
					}
				})
				if rec := recordTaxImportPreview(body); rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestTaxImportPreviewRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{name: "version", field: "version", value: 2},
		{name: "old year", field: "year", value: 2024},
		{name: "future year", field: "year", value: 2027},
		{name: "zero property", field: "property_id", value: 0},
		{name: "negative property", field: "property_id", value: -1},
		{name: "unsafe property", field: "property_id", value: maxTaxImportPropertyID + 1},
		{name: "empty entries", field: "entries", value: []any{}},
		{name: "null entry", field: "entries", value: []any{nil}},
		{name: "empty source", field: "source", value: "  "},
		{name: "source too long", field: "source", value: strings.Repeat("a", 1001)},
		{name: "source utf8 length", field: "source", value: strings.Repeat("ä", 501)},
		{name: "source controls", field: "source", value: "\nsource"},
		{name: "source leading line separator", field: "source", value: "\u2028source"},
		{name: "source paragraph separator", field: "source", value: "source\u2029rest"},
		{name: "empty reviewer", field: "reviewer", value: ""},
		{name: "reviewer controls", field: "reviewer", value: "reviewer\u007f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := taxImportJSON(t, func(req map[string]any) { req[tc.field] = tc.value })
			if rec := recordTaxImportPreview(body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "id empty", field: "external_id", value: ""},
		{name: "id leading punctuation", field: "external_id", value: "_1"},
		{name: "id trailing whitespace", field: "external_id", value: "ext:1 "},
		{name: "id non ascii", field: "external_id", value: "ä1"},
		{name: "id too long", field: "external_id", value: strings.Repeat("a", 65)},
		{name: "id unsafe", field: "external_id", value: "../1"},
		{name: "kind unknown", field: "kind", value: "depreciation"},
		{name: "kind case alias", field: "kind", value: "Income_adjustment"},
		{name: "description empty", field: "description", value: "  "},
		{name: "description too long", field: "description", value: strings.Repeat("x", 1001)},
		{name: "description controls", field: "description", value: "x\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := taxImportJSON(t, func(req map[string]any) {
				req["entries"].([]any)[0].(map[string]any)[tc.field] = tc.value
			})
			if rec := recordTaxImportPreview(body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTaxImportPreviewExactAmounts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		want  string
		cents int64
	}{
		{input: "0", want: "0.00"},
		{input: "-0.00", want: "0.00"},
		{input: "0.01", want: "0.01", cents: 1},
		{input: "-0.01", want: "-0.01", cents: -1},
		{input: "001.2", want: "1.20", cents: 120},
		{input: "-99.99", want: "-99.99", cents: -9999},
		{input: "1000000000.00", want: "1000000000.00", cents: maxTaxReviewCents},
		{input: "-1000000000.00", want: "-1000000000.00", cents: -maxTaxReviewCents},
	} {
		t.Run(tc.input, func(t *testing.T) {
			for _, kind := range []string{"income_adjustment", "expense_adjustment"} {
				out, err := normalizeTaxImportEntry(taxImportEntryRequest{
					Kind: kind, AmountEUR: tc.input, Description: "External correction",
				})
				if err != nil || out.AmountEUR != tc.want || out.AmountCents != tc.cents {
					t.Fatalf("out=%+v err=%v", out, err)
				}
			}
		})
	}
	for _, input := range []string{
		"", "-", "--1", "+1", "1.001", "-1.001", "1e2", "1E2", "NaN", "Inf",
		" 1", "1 ", "1,00", ".01", "1.", "1_000", "١", "1000000000.01", "-1000000000.01",
	} {
		t.Run("invalid "+input, func(t *testing.T) {
			body := taxImportJSON(t, func(req map[string]any) {
				req["entries"].([]any)[0].(map[string]any)["amount_eur"] = input
			})
			if rec := recordTaxImportPreview(body); rec.Code != http.StatusBadRequest {
				t.Fatalf("accepted %q: %s", input, rec.Body.String())
			}
		})
	}
	for _, input := range []string{"-1", "-0", "-0.00"} {
		_, err := normalizeTaxImportEntry(taxImportEntryRequest{
			Kind: "external_depreciation", AmountEUR: input, Description: "External AfA",
		})
		if err == nil {
			t.Fatalf("negative AfA accepted: %s", input)
		}
	}
}

func TestTaxImportPreviewEntryAndSumLimits(t *testing.T) {
	t.Parallel()
	entry := func(id, kind, amount string) taxImportEntryRequest {
		return taxImportEntryRequest{ExternalID: id, Kind: kind, AmountEUR: amount, Description: "External value"}
	}
	req := taxImportRequest{
		Version: 1, Year: 2026, PropertyID: maxTaxImportPropertyID, Source: "Source", Reviewer: "Reviewer",
		Entries: []taxImportEntryRequest{},
	}
	for i := range maxTaxImportEntries {
		req.Entries = append(req.Entries, entry(fmt.Sprintf("id-%d", i), "income_adjustment", "0.01"))
	}
	out, err := buildTaxImportPreview(req)
	if err != nil || len(out.Entries) != 100 || out.TotalsCents.IncomeAdjustment != 100 {
		t.Fatalf("valid maximum entries: out=%+v err=%v", out, err)
	}
	req.Entries = append(req.Entries, entry("extra", "income_adjustment", "0.01"))
	if _, err := buildTaxImportPreview(req); err == nil {
		t.Fatal("accepted 101 entries")
	}
	req.Entries = []taxImportEntryRequest{
		entry("same", "income_adjustment", "1"), entry("same", "expense_adjustment", "1"),
	}
	if _, err := buildTaxImportPreview(req); err == nil {
		t.Fatal("accepted duplicate external id across kinds")
	}
	req.Entries[1].ExternalID = "Same"
	if _, err := buildTaxImportPreview(req); err != nil {
		t.Fatalf("external ids must be case-sensitive: %v", err)
	}
	for _, kind := range []string{"income_adjustment", "expense_adjustment", "external_depreciation"} {
		req.Entries = []taxImportEntryRequest{entry("a", kind, "1000000000"), entry("b", kind, "0.01")}
		if _, err := buildTaxImportPreview(req); err == nil {
			t.Fatalf("accepted excessive sum for %s", kind)
		}
	}
	req.Entries = []taxImportEntryRequest{
		entry("a", "income_adjustment", "-1000000000"), entry("b", "income_adjustment", "-0.01"),
	}
	if _, err := buildTaxImportPreview(req); err == nil {
		t.Fatal("accepted excessive negative sum")
	}
	req.Entries = []taxImportEntryRequest{
		entry("a", "income_adjustment", "1000000000"), entry("b", "income_adjustment", "0.01"),
		entry("c", "income_adjustment", "-0.01"), entry("d", "expense_adjustment", "-1.23"),
		entry("e", "external_depreciation", "42.42"),
	}
	out, err = buildTaxImportPreview(req)
	wantTotals := taxImportTotals{
		IncomeAdjustment: maxTaxReviewCents, ExpenseAdjustment: -123, ExternalDepreciation: 4242,
	}
	if err != nil || out.TotalsCents != wantTotals {
		t.Fatalf("totals=%+v err=%v", out.TotalsCents, err)
	}
	req.Entries[0], req.Entries[2] = req.Entries[2], req.Entries[0]
	reordered, err := buildTaxImportPreview(req)
	if err != nil || reordered.TotalsCents != out.TotalsCents {
		t.Fatalf("entry order affects totals: %+v err=%v", reordered.TotalsCents, err)
	}
}

func TestTaxImportPreviewHTTPBoundary(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var h *Handler
		req := httptest.NewRequest(method, "/", strings.NewReader(validTaxImportJSON))
		rec := httptest.NewRecorder()
		h.TaxImportPreview(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("method %s: %d", method, rec.Code)
		}
	}
	for _, ct := range []string{"text/plain", "application/json; invalid"} {
		var h *Handler
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(validTaxImportJSON))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		h.TaxImportPreview(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("content type %s accepted: %d", ct, rec.Code)
		}
	}
	// A request of exactly 64 KiB remains valid; one extra whitespace byte fails.
	limitBody := validTaxImportJSON + strings.Repeat(" ", maxTaxImportBody-len(validTaxImportJSON))
	if rec := recordTaxImportPreview(limitBody); rec.Code != http.StatusOK {
		t.Fatalf("limit body rejected: %s", rec.Body.String())
	}
	if rec := recordTaxImportPreview(limitBody + " "); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit body accepted: %d", rec.Code)
	}
	// Preview output is not an apply/import payload: derived fields stay unknown.
	first := recordTaxImportPreview(validTaxImportJSON)
	if rec := recordTaxImportPreview(first.Body.String()); rec.Code != http.StatusBadRequest {
		t.Fatal("response accepted as trusted input")
	}
}

func TestTaxImportPreviewDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	var req taxImportRequest
	if err := json.Unmarshal([]byte(validTaxImportJSON), &req); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildTaxImportPreview(req)
	if err != nil {
		t.Fatal(err)
	}
	first.Entries[0].AmountCents = 999
	second, err := buildTaxImportPreview(req)
	if err != nil || second.Entries[0].AmountCents != 1230 {
		t.Fatalf("shared result state: %+v %v", second, err)
	}
	after, err := json.Marshal(req)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("input was changed: %s %v", after, err)
	}
}
