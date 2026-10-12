package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func taxReviewPointer[T any](value T) *T { return &value }

func TestTaxReviewCents(t *testing.T) {
	t.Parallel()
	if got, err := taxReviewCents(nil); err != nil || got != nil {
		t.Fatalf("missing amount = %v, %v; expected unknown", got, err)
	}
	for _, tt := range []struct {
		input    string
		expected int64
	}{
		{input: "0", expected: 0},
		{input: "0.00", expected: 0},
		{input: "0.01", expected: 1},
		{input: "1.2", expected: 120},
		{input: "001.02", expected: 102},
		{input: "55000.01", expected: 5_500_001},
		{input: "60500.00", expected: 6_050_000},
		{input: "1000000000.00", expected: maxTaxReviewCents},
	} {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := taxReviewCents(&tt.input)
			if err != nil || got == nil {
				t.Fatalf("parse = %v, %v", got, err)
			}
			if *got != tt.expected {
				t.Errorf("cents = %d; expected %d", *got, tt.expected)
			}
		})
	}
	for _, input := range []string{
		"", " ", " 1", "1 ", ".01", "1.", "-1", "+1", "1e3", "NaN", "Inf",
		"1,00", "1,000.00", "1.001", "1.2.3", "１.00", "1000000000.01",
		"1000000001", "9223372036854775807", "9999999999999", "1\n",
	} {
		t.Run("reject "+input, func(t *testing.T) {
			t.Parallel()
			if got, err := taxReviewCents(&input); err == nil || got != nil {
				t.Errorf("invalid amount = %v, %v", got, err)
			}
		})
	}
}

func TestTaxReviewTurnover(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		current  *string
		previous *string
		optedOut *bool
		expected string
	}{
		{name: "all missing", expected: "unknown"},
		{name: "missing current", previous: taxReviewPointer("0"), optedOut: taxReviewPointer(false), expected: "unknown"},
		{name: "missing previous", current: taxReviewPointer("0"), optedOut: taxReviewPointer(false), expected: "unknown"},
		{name: "missing option", current: taxReviewPointer("0"), previous: taxReviewPointer("0"), expected: "unknown"},
		{
			name:     "explicit zero",
			current:  taxReviewPointer("0"),
			previous: taxReviewPointer("0"),
			optedOut: taxReviewPointer(false),
			expected: "within_limit",
		},
		{
			name:     "exact limit",
			current:  taxReviewPointer("55000"),
			previous: taxReviewPointer("55000"),
			optedOut: taxReviewPointer(false),
			expected: "within_limit",
		},
		{
			name:     "first tolerance cent",
			current:  taxReviewPointer("55000.01"),
			previous: taxReviewPointer("55000"),
			optedOut: taxReviewPointer(false),
			expected: "within_tolerance",
		},
		{
			name:     "exact tolerance",
			current:  taxReviewPointer("60500"),
			previous: taxReviewPointer("55000"),
			optedOut: taxReviewPointer(false),
			expected: "within_tolerance",
		},
		{
			name:     "over tolerance",
			current:  taxReviewPointer("60500.01"),
			previous: taxReviewPointer("0"),
			optedOut: taxReviewPointer(false),
			expected: "current_limit_exceeded",
		},
		{
			name:     "prior excludes low current",
			current:  taxReviewPointer("0"),
			previous: taxReviewPointer("55000.01"),
			optedOut: taxReviewPointer(false),
			expected: "previous_limit_exceeded",
		},
		{
			name:     "prior excludes tolerance",
			current:  taxReviewPointer("60500"),
			previous: taxReviewPointer("60500"),
			optedOut: taxReviewPointer(false),
			expected: "previous_limit_exceeded",
		},
		{
			name:     "opted out",
			current:  taxReviewPointer("0"),
			previous: taxReviewPointer("0"),
			optedOut: taxReviewPointer(true),
			expected: "opted_out",
		},
		{name: "opted out but totals missing", optedOut: taxReviewPointer(true), expected: "opted_out"},
		{
			name:     "prior exceeded but current missing",
			previous: taxReviewPointer("60500"),
			optedOut: taxReviewPointer(false),
			expected: "previous_limit_exceeded",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := reviewTaxTurnover(2026, &taxReviewTurnoverRequest{
				CurrentEUR: tt.current, PreviousEUR: tt.previous, OptedOut: tt.optedOut,
				Source: " annual statement ", Reviewer: " reviewer ",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.expected || got.Verified || got.PreviousYear != 2025 {
				t.Errorf("result = %+v; expected unverified %s", got, tt.expected)
			}
			if got.Source != "annual statement" || got.Reviewer != "reviewer" || len(got.Notes) == 0 {
				t.Errorf("missing provenance/notes: %+v", got)
			}
		})
	}
	t.Run("receipts are only comparison", func(t *testing.T) {
		got, err := reviewTaxTurnover(2025, &taxReviewTurnoverRequest{ReceiptsEUR: taxReviewPointer("60500.01")})
		if err != nil || got.Status != "unknown" || got.CurrentCents != nil || got.DifferenceCents != nil {
			t.Fatalf("receipts inferred turnover: %+v, %v", got, err)
		}
		if got.ExternallySupplied || got.Verified || got.PreviousYear != 2024 {
			t.Errorf("receipts asserted fiscal provenance: %+v", got)
		}
	})
	t.Run("exact signed comparison", func(t *testing.T) {
		got, err := reviewTaxTurnover(2026, &taxReviewTurnoverRequest{
			CurrentEUR: taxReviewPointer("0.01"), ReceiptsEUR: taxReviewPointer("1.02"),
			Source: "statement", Reviewer: "reviewer",
		})
		if err != nil || got.DifferenceCents == nil {
			t.Fatalf("comparison = %+v, %v", got, err)
		}
		if *got.DifferenceCents != -101 || got.Status != "unknown" {
			t.Errorf("comparison = %+v", got)
		}
	})
}

func TestTaxReviewYearShift(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		paid         string
		due          string
		economicYear *int
		recurring    *bool
		expected     string
		hasError     bool
	}{
		{
			name:         "lower inclusive boundary",
			paid:         "2025-12-17",
			due:          "2026-01-01",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "candidate",
		},
		{
			name:         "upper inclusive boundary",
			paid:         "2026-01-15",
			due:          "2025-12-31",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "candidate",
		},
		{
			name:         "due lower inclusive",
			paid:         "2026-01-01",
			due:          "2025-12-17",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "candidate",
		},
		{
			name:         "due upper inclusive",
			paid:         "2025-12-31",
			due:          "2026-01-15",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "candidate",
		},
		{
			name:         "paid too early",
			paid:         "2025-12-16",
			due:          "2026-01-01",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "paid too late",
			paid:         "2026-01-16",
			due:          "2025-12-31",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "due too early",
			paid:         "2026-01-05",
			due:          "2025-12-16",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "due too late",
			paid:         "2025-12-31",
			due:          "2026-01-16",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "different boundaries",
			paid:         "2026-01-05",
			due:          "2025-01-05",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "wrong economic direction",
			paid:         "2025-01-05",
			due:          "2025-01-05",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "outside_window",
		},
		{
			name:         "two year distance",
			paid:         "2027-01-05",
			due:          "2027-01-05",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			hasError:     true,
		},
		{
			name:         "same year",
			paid:         "2026-06-01",
			due:          "2026-06-01",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "same_year",
		},
		{
			name:         "not recurring",
			paid:         "2026-01-05",
			due:          "2025-12-31",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(false),
			expected:     "not_recurring",
		},
		{
			name:         "missing due",
			paid:         "2026-01-05",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "unknown",
		},
		{
			name:         "same year missing due",
			paid:         "2026-06-01",
			economicYear: taxReviewPointer(2026),
			recurring:    taxReviewPointer(true),
			expected:     "unknown",
		},
		{
			name:         "missing paid",
			due:          "2025-12-31",
			economicYear: taxReviewPointer(2025),
			recurring:    taxReviewPointer(true),
			expected:     "unknown",
		},
		{
			name:      "missing economic year",
			paid:      "2026-01-05",
			due:       "2025-12-31",
			recurring: taxReviewPointer(true),
			expected:  "unknown",
		},
		{
			name:         "missing regularity",
			paid:         "2026-01-05",
			due:          "2025-12-31",
			economicYear: taxReviewPointer(2025),
			expected:     "unknown",
		},
		{
			name:         "unsupported economic year",
			paid:         "2025-01-05",
			due:          "2024-12-31",
			economicYear: taxReviewPointer(2024),
			recurring:    taxReviewPointer(true),
			hasError:     true,
		},
		{name: "invalid date", paid: "2026-02-30", hasError: true},
		{name: "date with time", paid: "2026-01-05T00:00:00Z", hasError: true},
		{name: "date without padding", paid: "2026-1-5", hasError: true},
		{name: "unrelated date", paid: "2030-01-05", hasError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, kind := range []string{"income", "expense"} {
				got, err := reviewTaxYearShift(2026, taxReviewShiftRequest{
					Kind: kind, PaidOn: tt.paid, DueOn: tt.due, EconomicYear: tt.economicYear,
					RegularlyRecurring: tt.recurring, Source: "contract and bank statement", Reviewer: "reviewer",
				})
				if (err != nil) != tt.hasError {
					t.Fatalf("%s: error = %v; expected error %t", kind, err, tt.hasError)
				}
				if tt.hasError {
					continue
				}
				if got.Status != tt.expected || got.Verified || len(got.Notes) == 0 {
					t.Errorf("%s: result = %+v; expected unverified %s", kind, got, tt.expected)
				}
				if tt.expected == "candidate" {
					if got.SuggestedYear == nil || *got.SuggestedYear != *tt.economicYear {
						t.Fatalf("candidate attribution = %+v", got)
					}
					if got.WindowFrom != "2025-12-17" || got.WindowTo != "2026-01-15" {
						t.Errorf("candidate window = %s to %s", got.WindowFrom, got.WindowTo)
					}
				} else if got.SuggestedYear != nil {
					t.Errorf("noncandidate suggested an allocation: %+v", got)
				}
			}
		})
	}
}

func TestTaxReviewPreview(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		body        string
		method      string
		contentType string
		expected    int
	}{
		{name: "empty optional values", body: `{"year":2025}`, expected: http.StatusOK},
		{
			name:     "explicit unknown",
			body:     `{"year":2026,"turnover":{"current_eur":null,"previous_eur":null,"opted_out":null}}`,
			expected: http.StatusOK,
		},
		{
			name:        "content type parameters",
			body:        `{"year":2026}`,
			contentType: "application/json; charset=utf-8",
			expected:    http.StatusOK,
		},
		{name: "trailing whitespace", body: "{\"year\":2026}\n\t ", expected: http.StatusOK},
		{name: "empty", body: "", expected: http.StatusBadRequest},
		{
			name:     "invalid utf8",
			body:     "{\"year\":2026,\"turnover\":{\"source\":\"\xff\"}}",
			expected: http.StatusBadRequest,
		},
		{
			name:     "unicode key alias",
			body:     `{"year":2026,"turnover":{"ſource":"statement"}}`,
			expected: http.StatusBadRequest,
		},
		{
			name:     "unicode key alias alongside canonical",
			body:     `{"year":2026,"turnover":{"source":"first","ſource":"replacement"}}`,
			expected: http.StatusBadRequest,
		},
		{name: "null", body: "null", expected: http.StatusBadRequest},
		{name: "array", body: `[]`, expected: http.StatusBadRequest},
		{name: "missing year", body: `{}`, expected: http.StatusBadRequest},
		{name: "old year", body: `{"year":2024}`, expected: http.StatusBadRequest},
		{name: "future year", body: `{"year":2027}`, expected: http.StatusBadRequest},
		{name: "fractional year", body: `{"year":2025.5}`, expected: http.StatusBadRequest},
		{name: "year overflow", body: `{"year":999999999999999999999}`, expected: http.StatusBadRequest},
		{name: "unknown field", body: `{"year":2026,"save":true}`, expected: http.StatusBadRequest},
		{name: "unknown nested field", body: `{"year":2026,"turnover":{"save":true}}`, expected: http.StatusBadRequest},
		{name: "truncated", body: `{"year":2026`, expected: http.StatusBadRequest},
		{name: "trailing object", body: `{"year":2026}{}`, expected: http.StatusBadRequest},
		{name: "trailing garbage", body: `{"year":2026}x`, expected: http.StatusBadRequest},
		{name: "duplicate year", body: `{"year":2025,"year":2026}`, expected: http.StatusBadRequest},
		{name: "duplicate year alias", body: `{"year":2025,"Year":2026}`, expected: http.StatusBadRequest},
		{name: "noncanonical field", body: `{"Year":2026}`, expected: http.StatusBadRequest},
		{
			name:     "duplicate nested alias",
			body:     `{"year":2026,"turnover":{"current_eur":"0","CURRENT_EUR":"60000"}}`,
			expected: http.StatusBadRequest,
		},
		{name: "duplicate escaped field", body: `{"year":2026,"ye\u0061r":2025}`, expected: http.StatusBadRequest},
		{name: "numeric amount", body: `{"year":2026,"turnover":{"current_eur":0}}`, expected: http.StatusBadRequest},
		{name: "blank amount", body: `{"year":2026,"turnover":{"current_eur":""}}`, expected: http.StatusBadRequest},
		{
			name:     "missing source",
			body:     `{"year":2026,"turnover":{"current_eur":"0","reviewer":"reviewer"}}`,
			expected: http.StatusBadRequest,
		},
		{
			name:     "missing reviewer",
			body:     `{"year":2026,"turnover":{"opted_out":false,"source":"statement"}}`,
			expected: http.StatusBadRequest,
		},
		{
			name:     "missing shift source",
			body:     `{"year":2026,"year_shift":{"kind":"income","paid_on":"2026-01-05"}}`,
			expected: http.StatusBadRequest,
		},
		{name: "invalid shift kind", body: `{"year":2026,"year_shift":{"kind":"refund"}}`, expected: http.StatusBadRequest},
		{name: "control in source", body: `{"year":2026,"turnover":{"source":"a\nb"}}`, expected: http.StatusBadRequest},
		{
			name:     "oversized source",
			body:     `{"year":2026,"turnover":{"source":"` + strings.Repeat("x", 1001) + `"}}`,
			expected: http.StatusBadRequest,
		},
		{
			name:     "deep nesting",
			body:     `{"year":2026,"turnover":` + strings.Repeat("[", 10) + `0` + strings.Repeat("]", 10) + `}`,
			expected: http.StatusBadRequest,
		},
		{
			name:     "body limit",
			body:     `{"year":2026}` + strings.Repeat(" ", maxTaxReviewBody),
			expected: http.StatusRequestEntityTooLarge,
		},
		{name: "wrong media type", body: `{"year":2026}`, contentType: "text/plain", expected: http.StatusBadRequest},
		{
			name:        "malformed media type",
			body:        `{"year":2026}`,
			contentType: "application/json; broken",
			expected:    http.StatusBadRequest,
		},
		{name: "wrong method", body: `{"year":2026}`, method: http.MethodGet, expected: http.StatusMethodNotAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			method := tt.method
			if method == "" {
				method = http.MethodPost
			}
			r := httptest.NewRequest(method, "/api/reports/tax-review/preview", strings.NewReader(tt.body))
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()
			// A nil receiver proves the preview never accesses Handler.DB/settings.
			var h *Handler
			h.TaxReviewPreview(w, r)
			if w.Code != tt.expected {
				t.Fatalf("status = %d; expected %d; body: %s", w.Code, tt.expected, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || !json.Valid(w.Body.Bytes()) {
				t.Errorf("response must be uncached valid json: %v %s", w.Header(), w.Body.String())
			}
			if tt.expected == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodPost {
				t.Error("missing allowed method")
			}
			if tt.expected != http.StatusOK {
				return
			}
			var got taxReviewResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Preview || got.Persisted || got.Turnover.Verified || got.Turnover.Status != "unknown" {
				t.Errorf("unsafe preview response: %+v", got)
			}
			if got.Turnover.CurrentCents != nil || got.Turnover.OptedOut != nil || len(got.Notes) == 0 {
				t.Errorf("unknown inputs were replaced: %+v", got)
			}
		})
	}
}

func TestTaxReviewPreviewComplete(t *testing.T) {
	t.Parallel()
	body := `{"year":2026,"turnover":{"current_eur":"60500.01","previous_eur":"55000.00",
		"receipts_eur":"60500.00","opted_out":false,"source":"annual statement","reviewer":"reviewer"},
		"year_shift":{"kind":"expense","economic_year":2025,"paid_on":"2026-01-15","due_on":"2025-12-31",
		"regularly_recurring":true,"source":"contract","reviewer":"reviewer"}}`
	r := httptest.NewRequest(http.MethodPost, "/api/reports/tax-review/preview", strings.NewReader(body))
	w := httptest.NewRecorder()
	var h *Handler
	h.TaxReviewPreview(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	var got taxReviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Preview || got.Persisted || got.Turnover.Verified || got.Turnover.Status != "current_limit_exceeded" {
		t.Errorf("unexpected turnover: %+v", got)
	}
	if got.Turnover.DifferenceCents == nil || *got.Turnover.DifferenceCents != 1 {
		t.Errorf("lost exact cent comparison: %+v", got.Turnover)
	}
	if got.YearShift == nil || got.YearShift.Verified || got.YearShift.Status != "candidate" {
		t.Fatalf("unexpected year shift: %+v", got.YearShift)
	}
	if got.YearShift.SuggestedYear == nil || *got.YearShift.SuggestedYear != 2025 {
		t.Errorf("candidate year = %+v", got.YearShift)
	}
}
