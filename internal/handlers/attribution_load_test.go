//go:build integration

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPaymentAttributionSyntheticLoad is deliberately opt-in: it measures two
// synthetic shapes, not production capacity or an SLA, and asserts only exact
// page equivalence. It never changes schema or indexes.
func TestPaymentAttributionSyntheticLoad(t *testing.T) {
	if os.Getenv("PARKRR_RUN_LOAD_TESTS") != "1" {
		t.Skip("synthetic workload requires PARKRR_RUN_LOAD_TESTS=1 and dedicated parkrr_load_test")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("PARKRR_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "parkrr_load_test" || cfg.ConnConfig.Port != 55444 ||
		(cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "127.0.0.1") {
		t.Fatal("load fixtures are restricted to dedicated localhost:55444/parkrr_load_test")
	}
	for _, profile := range []struct {
		name                 string
		customers, contracts int
		months, startYear    int
	}{
		{name: "many_small_customers", customers: 200, contracts: 1, months: 12, startYear: 2025},
		{name: "one_history_heavy_customer", customers: 1, contracts: 10, months: 240, startYear: 2006},
	} {
		t.Run(profile.name, func(t *testing.T) {
			h := testHandler(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			var count int
			if err := h.Pool.QueryRow(ctx, `SELECT count(*) FROM persons`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("load database must have no customer records before fixtures: count=%d, err=%v", count, err)
			}
			personIDs := seedAttributionLoad(t, h, profile.customers, profile.contracts, profile.months, profile.startYear)
			// Synthetic planner statistics only. No DDL/index changes and no timing assertion.
			if _, err := h.Pool.Exec(ctx, `ANALYZE persons,payments,flat_rate_periods,recurring_charges,charges,payment_allocations`); err != nil {
				t.Fatal(err)
			}
			collector := &attributionLoadTrace{}
			poolConfig := h.Pool.Config()
			poolConfig.MaxConns = 4
			poolConfig.ConnConfig.Tracer = collector
			pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			measured := New(pool)
			report := attributionLoadReport{
				Profile: profile.name, Synthetic: true, Samples: 61, PageLimit: 25,
				Customers: profile.customers, PaymentsPerCustomer: profile.contracts * profile.months * 3,
				GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
				PoolMaxConns: 4, CreatedAt: time.Now().UTC().Format(time.RFC3339),
				Caveat: "Synthetic warm-cache sequential local workload; not representative production latency, concurrency, capacity, or an SLA. Agreement, recurring and charge attribution with some reversed receipts; vehicle/invoice attribution is not populated. Three unmeasured warm-ups; alternating execution order; no timing thresholds. Existing indexes only.",
			}
			if err := h.Pool.QueryRow(ctx, `SELECT version()`).Scan(&report.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			var pageTimes, fullTimes []float64
			for sample := -3; sample < report.Samples; sample++ {
				i := max(sample, 0)
				personID := personIDs[(i*37)%len(personIDs)]
				offsets := []int{0, report.PaymentsPerCustomer / 2, max(0, report.PaymentsPerCustomer-report.PageLimit)}
				offset := offsets[i%len(offsets)]
				request := func() *http.Request {
					req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/payments?limit=%d&offset=%d", report.PageLimit, offset), nil).WithContext(ctx)
					req.SetPathValue("id", strconv.FormatInt(personID, 10))
					return req
				}
				var actual, reference *httptest.ResponseRecorder
				var pageMS, fullMS float64
				runPage := func() {
					actual = httptest.NewRecorder()
					start := time.Now()
					measured.ListPayments(actual, request())
					pageMS = float64(time.Since(start)) / float64(time.Millisecond)
				}
				runFull := func() {
					reference = httptest.NewRecorder()
					start := time.Now()
					legacyAttributionLoadPage(measured, reference, request())
					fullMS = float64(time.Since(start)) / float64(time.Millisecond)
				}
				if i%2 == 0 {
					runPage()
					runFull()
				} else {
					runFull()
					runPage()
				}
				if actual.Code != http.StatusOK || reference.Code != http.StatusOK {
					t.Fatalf("load response errors: page=%d %s; reference=%d %s", actual.Code, actual.Body, reference.Code, reference.Body)
				}
				var page, expected []payment
				if err := json.Unmarshal(actual.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(reference.Body.Bytes(), &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(page, expected) || actual.Header().Get("X-Total-Count") != reference.Header().Get("X-Total-Count") {
					t.Fatalf("payment IDs/order/fields/attributed amounts differ at customer %d offset %d", personID, offset)
				}
				if actual.Header().Get("X-Total-Count") != strconv.Itoa(report.PaymentsPerCustomer) || len(page) == 0 {
					t.Fatalf("fixture missing expected receipts: total=%s page=%d expected=%d", actual.Header().Get("X-Total-Count"), len(page), report.PaymentsPerCustomer)
				}
				if len(page) > report.PageLimit {
					t.Fatalf("page limit exceeded: %d", len(page))
				}
				if sample >= 0 {
					pageTimes, fullTimes = append(pageTimes, pageMS), append(fullTimes, fullMS)
					report.ComparedRows += len(page)
					for _, p := range page {
						report.ComparedPaymentCents += int64(math.Round(p.Amount * 100))
						for _, item := range p.Items {
							report.ComparedAllocationCents += int64(math.Round(item.Amount * 100))
						}
					}
				}
			}
			report.PageP50MS, report.PageP95MS = attributionLoadPercentiles(pageTimes)
			report.FullP50MS, report.FullP95MS = attributionLoadPercentiles(fullTimes)
			report.PageSamplesMS, report.FullSamplesMS = pageTimes, fullTimes
			for _, mode := range []string{"page", "full_history_reference"} {
				traceCtx := context.WithValue(ctx, attributionLoadTraceKey{}, mode)
				req := httptest.NewRequest(http.MethodGet, "/payments?limit=25&offset=0", nil).WithContext(traceCtx)
				req.SetPathValue("id", strconv.FormatInt(personIDs[0], 10))
				rec := httptest.NewRecorder()
				if mode == "page" {
					measured.ListPayments(rec, req)
				} else {
					legacyAttributionLoadPage(measured, rec, req)
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("traced response: %d %s", rec.Code, rec.Body)
				}
			}
			for _, query := range collector.queries() {
				var plan json.RawMessage
				if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.args...).Scan(&plan); err != nil {
					t.Fatalf("explain %s: %v", query.Mode, err)
				}
				query.Plan = plan
				report.Plans = append(report.Plans, query)
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.ArtifactDir(), "synthetic-payment-load.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("SYNTHETIC %s: customers=%d payments=%d samples=%d compared_rows=%d page p50/p95=%.3f/%.3f ms full-history p50/p95=%.3f/%.3f ms; exact outcomes equal; artifact=%s",
				report.Profile, report.Customers, report.Customers*report.PaymentsPerCustomer, report.Samples, report.ComparedRows,
				report.PageP50MS, report.PageP95MS, report.FullP50MS, report.FullP95MS, path)
		})
	}
}

func seedAttributionLoad(t *testing.T, h *Handler, customers, contracts, months, startYear int) []int64 {
	t.Helper()
	ctx := t.Context()
	rows, err := h.Pool.Query(ctx, `INSERT INTO persons(first_name,last_name)
		SELECT 'Synthetic load '||n,'Integration' FROM generate_series(1,$1) n RETURNING id`, customers)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	start := fmt.Sprintf("%04d-01-01", startYear)
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO flat_rate_periods(person_id,amount,period,start_date,note)
			SELECT p,30,'monthly',$2::date,'synthetic contract '||n FROM unnest($1::bigint[]) p CROSS JOIN generate_series(1,$3) n`, []any{ids, start, contracts}},
		{`INSERT INTO recurring_charges(person_id,description,amount,period,start_date)
			SELECT p,'synthetic utilities '||n,10,'monthly',$2::date FROM unnest($1::bigint[]) p CROSS JOIN generate_series(1,$3) n`, []any{ids, start, contracts}},
		{`INSERT INTO payments(person_id,amount,paid_on,method,settles_kind,settles_ref,settles_period,reversed)
			SELECT a.person_id,30+(n%17)::numeric/100,($2::date+n*interval '1 month')::date,'bar','agreement',a.id,
			to_char($2::date+n*interval '1 month','YYYY-MM'), n%17=16
			FROM flat_rate_periods a CROSS JOIN generate_series(0,$3-1) n WHERE a.person_id=ANY($1)`, []any{ids, start, months}},
		{`INSERT INTO payments(person_id,amount,paid_on,method,settles_kind,settles_ref,settles_period,reversed)
			SELECT a.person_id,10+(n%13)::numeric/100,($2::date+n*interval '1 month'+interval '1 day')::date,'ueberweisung','recurring',a.id,
			to_char($2::date+n*interval '1 month','YYYY-MM'),n%17=16
			FROM recurring_charges a CROSS JOIN generate_series(0,$3-1) n WHERE a.person_id=ANY($1)`, []any{ids, start, months}},
		{`INSERT INTO charges(person_id,description,amount,charged_on)
			SELECT a.person_id,'synthetic extra '||a.id||':'||n,5+(n%11)::numeric/100,($2::date+n*interval '1 month'+interval '2 days')::date
			FROM flat_rate_periods a CROSS JOIN generate_series(0,$3-1) n WHERE a.person_id=ANY($1)`, []any{ids, start, months}},
		{`INSERT INTO payments(person_id,amount,paid_on,method,note)
			SELECT c.person_id,c.amount,c.charged_on,'bar','load-charge:'||c.id FROM charges c WHERE c.person_id=ANY($1)`, []any{ids}},
		{`INSERT INTO payment_allocations(payment_id,kind,ref_id,amount)
			SELECT p.id,'charge',c.id,c.amount FROM payments p JOIN charges c ON p.person_id=c.person_id AND p.note='load-charge:'||c.id
			WHERE p.person_id=ANY($1)`, []any{ids}},
	}
	for _, statement := range statements {
		if _, err := h.Pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// Test-only reference keeps the exact list/count/serialization work but performs
// the former full-history attribution before projecting it onto the page.
func legacyAttributionLoadPage(h *Handler, w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	limit, offset := pageParams(r, 1000, 5000)
	h.totalCount(w, r.Context(), `SELECT count(*) FROM payments WHERE person_id=$1`, id)
	rows, err := h.Pool.Query(r.Context(), `SELECT `+paymentColumns+` FROM payments WHERE person_id=$1 ORDER BY paid_on DESC,id DESC LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		serverError(w, r, "load reference query failed", err)
		return
	}
	out := []payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			rows.Close()
			serverError(w, r, "load reference scan failed", err)
			return
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		serverError(w, r, "load reference iteration failed", err)
		return
	}
	items, err := h.resolvePaymentItems(r.Context(), id)
	if err != nil {
		serverError(w, r, "load reference attribution failed", err)
		return
	}
	for i := range out {
		out[i].Items = items[out[i].ID]
	}
	writeJSON(w, http.StatusOK, out)
}

func attributionLoadPercentiles(samples []float64) (float64, float64) {
	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	return ordered[int(math.Ceil(float64(len(ordered))*0.50))-1], ordered[int(math.Ceil(float64(len(ordered))*0.95))-1]
}

type attributionLoadReport struct {
	Profile                 string                 `json:"profile"`
	Synthetic               bool                   `json:"synthetic"`
	Caveat                  string                 `json:"caveat"`
	CreatedAt               string                 `json:"created_at"`
	GoVersion               string                 `json:"go_version"`
	Platform                string                 `json:"platform"`
	PostgreSQL              string                 `json:"postgresql"`
	PoolMaxConns            int32                  `json:"pool_max_conns"`
	Customers               int                    `json:"customers"`
	PaymentsPerCustomer     int                    `json:"payments_per_customer"`
	Samples                 int                    `json:"samples"`
	PageLimit               int                    `json:"page_limit"`
	ComparedRows            int                    `json:"compared_rows"`
	ComparedPaymentCents    int64                  `json:"compared_payment_cents"`
	ComparedAllocationCents int64                  `json:"compared_allocation_cents"`
	PageP50MS               float64                `json:"page_p50_ms"`
	PageP95MS               float64                `json:"page_p95_ms"`
	FullP50MS               float64                `json:"full_p50_ms"`
	FullP95MS               float64                `json:"full_p95_ms"`
	PageSamplesMS           []float64              `json:"page_samples_ms"`
	FullSamplesMS           []float64              `json:"full_samples_ms"`
	Plans                   []attributionLoadQuery `json:"plans"`
}

type attributionLoadQuery struct {
	Mode string          `json:"mode"`
	SQL  string          `json:"sql"`
	Plan json.RawMessage `json:"explain_analyze_buffers"`
	args []any
}

type attributionLoadTraceKey struct{}

type attributionLoadTrace struct {
	mu     sync.Mutex
	traces []attributionLoadQuery
}

func (t *attributionLoadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	mode, ok := ctx.Value(attributionLoadTraceKey{}).(string)
	if !ok || !strings.HasPrefix(strings.TrimSpace(data.SQL), "SELECT") {
		return ctx
	}
	t.mu.Lock()
	t.traces = append(t.traces, attributionLoadQuery{Mode: mode, SQL: data.SQL, args: slices.Clone(data.Args)})
	t.mu.Unlock()
	return ctx
}

func (t *attributionLoadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *attributionLoadTrace) queries() []attributionLoadQuery {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.traces)
}
