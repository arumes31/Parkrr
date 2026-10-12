package handlers

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseTaxDateLengthBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
		tooLong bool
	}{
		{name: "canonical", input: "2025-01-01", want: "2025-01-01"},
		{name: "short surrounding whitespace", input: " \t2025-01-01\n", want: "2025-01-01"},
		{
			name: "padded at cap", input: "2025-01-01" + strings.Repeat(" ", maxDateLen-10),
			want: "2025-01-01",
		},
		{
			name: "padded over cap", input: "2025-01-01" + strings.Repeat(" ", maxDateLen-9),
			wantErr: true, tooLong: true,
		},
		{
			name: "leading whitespace over cap", input: strings.Repeat(" ", maxDateLen) + "2025-01-01",
			wantErr: true, tooLong: true,
		},
		{name: "long whitespace", input: strings.Repeat(" ", maxDateLen+1), wantErr: true, tooLong: true},
		{name: "long malformed date", input: strings.Repeat("2", maxDateLen+1), wantErr: true, tooLong: true},
		{name: "leap date", input: "2024-02-29", want: "2024-02-29"},
		{name: "invalid leap date", input: "2025-02-29", wantErr: true},
		{name: "invalid format", input: "01.01.2025", wantErr: true},
		{name: "required empty", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseTaxDate(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseTaxDate error = %v, want error %v", err, tc.wantErr)
			}
			if tc.tooLong && err.Error() != "date is too long" {
				t.Fatalf("length error = %q, want date is too long", err)
			}
			if tc.wantErr {
				if !got.IsZero() {
					t.Fatalf("invalid date returned %v instead of zero time", got)
				}
				return
			}
			if got.Format("2006-01-02") != tc.want || got.Location() != time.Local {
				t.Fatalf("date = %v, want %s in local timezone", got, tc.want)
			}
		})
	}
}

func TestScanTaxIDLengthBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  int64
		ok    bool
	}{
		{name: "positive", input: "1", want: 1, ok: true},
		{name: "leading zeroes at cap", input: strings.Repeat("0", maxNumericParamLen-1) + "1", want: 1, ok: true},
		{name: "leading zeroes over cap", input: strings.Repeat("0", maxNumericParamLen) + "1"},
		{name: "zero", input: "0"},
		{name: "negative", input: "-1"},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "/tax-resource", nil)
			req.SetPathValue("id", tc.input)
			got, ok := scanTaxID(req)
			if ok != tc.ok {
				t.Fatalf("scanTaxID valid = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("scanTaxID = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNullableDateLengthBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty"},
		{name: "blank at cap", input: strings.Repeat(" ", maxDateLen)},
		{name: "blank over cap", input: strings.Repeat(" ", maxDateLen+1), wantErr: true},
		{name: "canonical", input: "2025-01-01", want: "2025-01-01"},
		{name: "trimmed", input: " 2025-01-01 ", want: "2025-01-01"},
		{
			name: "padded over cap", input: "2025-01-01" + strings.Repeat(" ", maxDateLen-9),
			wantErr: true,
		},
		{name: "invalid calendar date", input: "2025-02-29", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := nullableDate(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("nullableDate error = %v, want error %v", err, tc.wantErr)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("optional date = %v, want nil", got)
				}
				return
			}
			if got == nil || got.Format("2006-01-02") != tc.want {
				t.Fatalf("optional date = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestTaxYearPathLengthBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  int
		ok    bool
	}{
		{name: "minimum", input: "2000", want: 2000, ok: true},
		{name: "maximum", input: "2100", want: 2100, ok: true},
		{name: "leading zeroes at cap", input: strings.Repeat("0", maxNumericParamLen-4) + "2025", want: 2025, ok: true},
		{name: "leading zeroes over cap", input: strings.Repeat("0", maxNumericParamLen-3) + "2025"},
		{name: "below range", input: "1999", want: 1999},
		{name: "above range", input: "2101", want: 2101},
		{name: "non numeric", input: "year"},
		{name: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("POST", "/tax-year", nil)
			req.SetPathValue("year", tc.input)
			got, ok := taxYearPath(req)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("taxYearPath = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
