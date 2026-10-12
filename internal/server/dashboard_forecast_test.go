//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/preining/parkrr/internal/auth"
	"github.com/preining/parkrr/internal/backup"
	"github.com/preining/parkrr/internal/mail"
)

type forecastRouteResponse struct {
	Year                int       `json:"year"`
	CurrentYear         int       `json:"current_year"`
	AsOf                string    `json:"as_of"`
	AvailableYears      []int     `json:"available_years"`
	ForecastYears       []int     `json:"forecast_years"`
	HasYearData         bool      `json:"has_year_data"`
	ActualByMonth       []float64 `json:"actual_by_month"`
	ForecastByMonth     []float64 `json:"forecast_by_month"`
	ReservationsByMonth []float64 `json:"reservations_by_month"`
	PreviousByMonth     []float64 `json:"previous_by_month"`
	Years               []struct {
		Year         int     `json:"year"`
		Actual       float64 `json:"actual"`
		Forecast     float64 `json:"forecast"`
		Reservations float64 `json:"reservations"`
		HasData      bool    `json:"has_data"`
	} `json:"years"`
	Assumptions []string `json:"assumptions"`
	Warnings    []string `json:"warnings"`
}

func TestDashboardForecastRouteIntegration(t *testing.T) {
	// Like the other full-router integration tests, use a disposable database
	// without parallel writers: the audit and invoice assertions are global.
	pool := testPool(t)
	manager, err := auth.NewManager(
		pool,
		auth.SessionConfig{MaxAge: 3600},
		false,
		false,
		"dashboard-forecast-test-session-secret-long-enough",
	)
	if err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthnService(
		pool,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// The GET must be pure; unrelated archival/snapshot/backfill workers must not
	// introduce writes or race with the clock pinned below.
	handler, h, err := New(
		pool,
		manager,
		wa,
		0,
		"",
		false,
		false,
		false,
		"",
		"",
		"",
		backup.S3Config{},
		mail.New(mail.Config{}),
		"",
		stop,
		func(func()) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	h.Now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	if h.TaxWritesEnabled {
		t.Fatal("forecast fixture must retain the default tax read-only mode")
	}
	personID := seedForecastRouteData(t, pool)
	cookiesFor := func(role string) []*http.Cookie {
		t.Helper()
		var id int64
		if err := pool.QueryRow(
			t.Context(),
			`INSERT INTO users (username,email,password_hash,role,is_admin)
			 VALUES ($1,'','unused',$2,$3) RETURNING id`,
			fmt.Sprintf("forecast-%s-%d", role, time.Now().UnixNano()),
			role,
			role == "admin",
		).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
		rec := httptest.NewRecorder()
		if err := manager.CreateSession(
			t.Context(),
			rec,
			httptest.NewRequest(http.MethodGet, "/", nil),
			id,
		); err != nil {
			t.Fatal(err)
		}
		return rec.Result().Cookies()
	}
	admin, editor, reader := cookiesFor("admin"), cookiesFor("editor"), cookiesFor("reader")
	request := func(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		// A read must not require the state-changing CSRF header.
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	before := forecastRouteFinancialState(t, pool, personID)
	var firstResponse string
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		want    int
	}{
		{name: "anonymous", cookies: []*http.Cookie{}, want: http.StatusUnauthorized},
		{name: "admin", cookies: admin, want: http.StatusOK},
		{name: "editor", cookies: editor, want: http.StatusOK},
		{name: "reader", cookies: reader, want: http.StatusOK},
		{name: "reader repeated GET", cookies: reader, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request("/api/overview/forecast?year=2026", tc.cookies)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			checkForecastRouteResponse(t, rec, 2026)
			if firstResponse == "" {
				firstResponse = rec.Body.String()
			}
			if rec.Body.String() != firstResponse {
				t.Fatal("identical read changed the forecast by role or repetition")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "default", want: 2026},
		{name: "malformed", query: "?year=oops", want: 2026},
		{name: "below range", query: "?year=1999", want: 2026},
		{name: "above range", query: "?year=2101", want: 2026},
		{name: "overlong", query: "?year=" + strings.Repeat("9", 100), want: 2026},
		{name: "unavailable horizon", query: "?year=2028", want: 2026},
		{name: "known history", query: "?year=2001", want: 2001},
		{name: "next year", query: "?year=2027", want: 2027},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := checkForecastRouteResponse(t, request("/api/overview/forecast"+tc.query, reader), tc.want)
			if tc.want == 2001 && !rep.HasYearData {
				t.Fatal("archived zero-rate history was treated as unavailable")
			}
			if tc.want == 2027 && rep.PreviousByMonth != nil {
				t.Fatal("unfinished current year must not masquerade as a complete prior year")
			}
		})
	}
	for _, tc := range []struct {
		name string
		path string
		text string
	}{
		{name: "forecast module", path: "/js/revenue-chart.js", text: "export function createRevenueChart"},
		{name: "app module import", path: "/js/app.js", text: "./revenue-chart.js"},
		{name: "service worker shell", path: "/sw.js", text: "/js/revenue-chart.js"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(tc.path, nil)
			mediaType, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
			if rec.Code != http.StatusOK || err != nil {
				t.Fatalf("asset unavailable: status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
			}
			if mediaType != "application/javascript" && mediaType != "text/javascript" {
				t.Fatalf("JavaScript route returned %q (possibly the SPA fallback)", mediaType)
			}
			if !strings.Contains(rec.Body.String(), tc.text) {
				t.Fatalf("asset %s lacks %q", tc.path, tc.text)
			}
		})
	}
	if after := forecastRouteFinancialState(t, pool, personID); after != before {
		t.Fatal("forecast or asset GET changed financial/source data, invoice numbering or audit history")
	}
}

func checkForecastRouteResponse(t *testing.T, rec *httptest.ResponseRecorder, wantYear int) forecastRouteResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("forecast status=%d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("financial forecast response must be no-store")
	}
	mediaType, _, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("forecast content-type=%q", rec.Header().Get("Content-Type"))
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"year", "current_year", "as_of", "available_years", "forecast_years", "has_year_data",
		"actual_by_month", "forecast_by_month", "reservations_by_month", "previous_by_month",
		"years", "assumptions", "warnings",
	} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("missing JSON contract field %q", field)
		}
	}
	var rep forecastRouteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Year != wantYear || rep.CurrentYear != 2026 || rep.AsOf != "2026-10-09" {
		t.Fatalf("unexpected scope: year=%d current=%d as_of=%q", rep.Year, rep.CurrentYear, rep.AsOf)
	}
	if !slices.IsSorted(rep.AvailableYears) || !slices.Contains(rep.AvailableYears, 2026) {
		t.Fatalf("invalid available years: %v", rep.AvailableYears)
	}
	if !slices.Contains(rep.AvailableYears, 2001) || slices.Contains(rep.AvailableYears, 2027) {
		t.Fatalf("lost actual history or included a forecast-only year: %v", rep.AvailableYears)
	}
	wantForecastYears := append(slices.Clone(rep.AvailableYears), 2027)
	if !slices.Equal(rep.ForecastYears, wantForecastYears) || len(rep.Years) != len(rep.ForecastYears) {
		t.Fatalf("invalid forecast-year contract: %v / %d rows", rep.ForecastYears, len(rep.Years))
	}
	monthGroups := [][]float64{rep.ActualByMonth, rep.ForecastByMonth, rep.ReservationsByMonth}
	// null deliberately means unavailable; an available zero year must remain an array.
	if rep.PreviousByMonth != nil {
		monthGroups = append(monthGroups, rep.PreviousByMonth)
	}
	for _, months := range monthGroups {
		if len(months) != 12 {
			t.Fatalf("expected 12 months, got %d", len(months))
		}
		for _, value := range months {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value*100-math.Round(value*100)) > 0.001 {
				t.Fatalf("non-finite or subcent monthly amount %v", value)
			}
		}
	}
	if len(rep.Assumptions) == 0 || rep.Warnings == nil {
		t.Fatal("assumptions/warnings must be explicit arrays")
	}
	selected := false
	for i, row := range rep.Years {
		if row.Year != rep.ForecastYears[i] {
			t.Fatal("annual rows and forecast years disagree")
		}
		if row.Year != rep.Year {
			continue
		}
		selected = true
		for j, amount := range []float64{row.Actual, row.Forecast, row.Reservations} {
			var cents int64
			for _, value := range monthGroups[j] {
				cents += int64(math.Round(value * 100))
			}
			if cents != int64(math.Round(amount*100)) {
				t.Fatalf("annual value %v differs from monthly cents %d", amount, cents)
			}
		}
		if row.HasData != rep.HasYearData {
			t.Fatal("selected annual row and has_year_data disagree")
		}
	}
	if !selected {
		t.Fatal("selected year is missing from annual rows")
	}
	return rep
}

func seedForecastRouteData(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var personID, categoryID int64
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if personID != 0 {
			if _, err := pool.Exec(ctx, `DELETE FROM persons WHERE id=$1`, personID); err != nil {
				t.Error(err)
			}
		}
		if categoryID != 0 {
			if _, err := pool.Exec(ctx, `DELETE FROM categories WHERE id=$1`, categoryID); err != nil {
				t.Error(err)
			}
		}
	})
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO persons (first_name,last_name) VALUES ('Forecast','ServerForecast') RETURNING id`,
	).Scan(&personID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(),
		`INSERT INTO categories (name,default_monthly_cost) VALUES ($1,99) RETURNING id`,
		fmt.Sprintf("forecast-route-%d", personID),
	).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		t.Context(),
		`INSERT INTO vehicles (person_id,category_id,rate,cost_override,start_date,end_date,status,archived)
		 VALUES ($1,$2,0,0,'2001-01-01','2001-12-31','collected',true),
		        ($1,$2,31,NULL,'2026-01-01','2027-12-31','stored',false)`,
		personID,
		categoryID,
	); err != nil {
		t.Fatal(err)
	}
	// Synthetic automatic receipt permits ordinary fixture cleanup without
	// disabling immutability guards or creating a real invoice workflow.
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO payments (person_id,amount,paid_on,method,note,auto)
		 VALUES ($1,13.37,'2026-01-01','bar','forecast route fixture',true)`, personID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO charges (person_id,description,amount,charged_on)
		 VALUES ($1,'forecast route fixture',2.50,'2026-10-09')`, personID,
	); err != nil {
		t.Fatal(err)
	}
	return personID
}

func forecastRouteFinancialState(t *testing.T, pool *pgxpool.Pool, personID int64) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'person', (SELECT to_jsonb(p) FROM persons p WHERE id=$1),
		'vehicles', (SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM vehicles v WHERE person_id=$1),
		'charges', (SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM charges c WHERE person_id=$1),
		'payments', (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payments p WHERE person_id=$1),
		'invoices', (SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
		'billing', (SELECT to_jsonb(b) FROM billing_settings b WHERE id=1),
		'payment_count', (SELECT count(*) FROM payments),
		'allocation_count', (SELECT count(*) FROM payment_allocations),
		'invoice_payment_count', (SELECT count(*) FROM invoice_payments),
		'invoice_source_count', (SELECT count(*) FROM invoice_source),
		'tax_expense_count', (SELECT count(*) FROM tax_expenses),
		'tax_asset_count', (SELECT count(*) FROM tax_assets),
		'audit_count', (SELECT count(*) FROM audit_log),
		'occupancy_count', (SELECT count(*) FROM occupancy_snapshots)
	)::text`, personID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
