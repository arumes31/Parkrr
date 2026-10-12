package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/preining/parkrr/internal/models"
)

func forecastDate(value string) time.Time {
	date, err := time.Parse(dateLayout, value)
	if err != nil {
		panic(err)
	}
	return date
}

func forecastDatePtr(value string) *time.Time {
	date := forecastDate(value)
	return &date
}

func forecastFixture() dashboardForecastData {
	return dashboardForecastData{
		Vehicles: []models.Vehicle{}, Categories: map[int64]models.Category{},
		Agreements: map[int64][]models.FlatRatePeriod{},
		Recurring:  []models.FlatRatePeriod{}, Charges: []dashboardForecastCharge{},
	}
}

func forecastTestYears(data dashboardForecastData, now time.Time) dashboardYears {
	ys := newDashboardYears(now)
	for _, vehicle := range data.Vehicles {
		ys.addSpan(vehicle.StartDate, vehicle.EndDate, now)
	}
	for _, agreements := range data.Agreements {
		for _, agreement := range agreements {
			ys.addSpan(agreement.StartDate, agreement.EndDate, now)
		}
	}
	for _, recurring := range data.Recurring {
		ys.addSpan(recurring.StartDate, recurring.EndDate, now)
	}
	for _, charge := range data.Charges {
		ys.addSpan(charge.On, &charge.On, now)
	}
	ys.finish(now.Year())
	return ys
}

func TestDashboardYearAvailabilityAndCanonicalization(t *testing.T) {
	t.Parallel()
	now := forecastDate("2026-10-09")
	ys := newDashboardYears(now)
	ys.addSpan(forecastDate("2020-12-31"), forecastDatePtr("2022-01-01"), now)
	ys.addSpan(forecastDate("2024-06-01"), forecastDatePtr("2024-06-01"), now)
	ys.addSpan(forecastDate("2027-01-01"), nil, now)
	ys.addSpan(forecastDate("2023-03-01"), forecastDatePtr("2023-02-01"), now)
	ys.finish(now.Year())
	if !reflect.DeepEqual(ys.Available, []int{2020, 2021, 2022, 2024, 2026}) {
		t.Fatalf("lost a spanned year or invented a gap: %+v", ys)
	}
	if ys.Present[2026] || ys.Present[2025] || !ys.Present[2024] {
		t.Fatalf("current-year fallback is not evidence of data: %+v", ys)
	}
	for _, tc := range []struct {
		name      string
		query     string
		allowNext bool
		want      int
	}{
		{name: "known", query: "?year=2024", want: 2024},
		{name: "real gap", query: "?year=2025", want: 2026},
		{name: "empty", want: 2026},
		{name: "malformed", query: "?year=oops", want: 2026},
		{name: "too early", query: "?year=1999", want: 2026},
		{name: "too late", query: "?year=2101", want: 2026},
		{name: "overlong", query: "?year=" + strings.Repeat("9", 100), want: 2026},
		{name: "future overview", query: "?year=2027", want: 2026},
		{name: "future forecast", query: "?year=2027", allowNext: true, want: 2027},
		{name: "far future forecast", query: "?year=2028", allowNext: true, want: 2026},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/overview"+tc.query, nil)
			got := ys.canonical(parseYearParam(req, now.Year()), now.Year(), tc.allowNext)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDashboardForecastEmptyAndZeroHistory(t *testing.T) {
	t.Parallel()
	now := forecastDate("2026-10-09")
	data := forecastFixture()
	rep := buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
	if rep.HasYearData || rep.PreviousByMonth != nil || !reflect.DeepEqual(rep.AvailableYears, []int{2026}) {
		t.Fatalf("empty state invented history: %+v", rep)
	}
	free := 0.0
	data.Categories[1] = models.Category{ID: 1, DefaultMonthlyCost: 99}
	data.Vehicles = []models.Vehicle{{
		ID: 1, CategoryID: 1, PersonID: 1, CostOverride: &free,
		BillingPeriod: models.BillingMonthly, StartDate: forecastDate("2025-01-01"),
		EndDate: forecastDatePtr("2025-12-31"), Status: models.StatusCollected, Archived: true,
	}}
	rep = buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
	if rep.PreviousByMonth == nil || sumForecastMonths(rep.PreviousByMonth) != 0 || !rep.Years[0].HasData {
		t.Fatalf("valid zero-priced archive treated as unavailable: %+v", rep)
	}
	data.Vehicles[0].StartDate = forecastDate("2024-01-01")
	data.Vehicles[0].EndDate = forecastDatePtr("2024-12-31")
	rep = buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
	if rep.PreviousByMonth != nil {
		t.Fatal("gap in 2025 silently substituted 2024")
	}
}

func TestDashboardForecastStoredReservationAndClosedStatuses(t *testing.T) {
	t.Parallel()
	now := forecastDate("2026-01-15")
	for _, tc := range []struct {
		name        string
		status      string
		archived    bool
		forecast    bool
		reservation bool
	}{
		{name: "stored", status: models.StatusStored, forecast: true},
		{name: "reserved", status: models.StatusReserved, reservation: true},
		{name: "cancelled", status: models.StatusCancelled},
		{name: "collected", status: models.StatusCollected},
		{name: "archived legacy stored", status: models.StatusStored, archived: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := forecastFixture()
			data.Vehicles = []models.Vehicle{{
				ID: 1, PersonID: 1, Rate: 31, BillingPeriod: models.BillingMonthly,
				StartDate: forecastDate("2026-01-01"), EndDate: forecastDatePtr("2026-01-31"),
				Status: tc.status, Archived: tc.archived,
			}}
			rep := buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
			if rep.ActualByMonth[0] != 15 {
				t.Fatalf("existing accrued semantics changed: %v", rep.ActualByMonth)
			}
			wantFuture, wantReservation := 0.0, 0.0
			if tc.forecast {
				wantFuture = 16
			}
			if tc.reservation {
				wantReservation = 16
			}
			if rep.ForecastByMonth[0] != wantFuture || rep.ReservationsByMonth[0] != wantReservation {
				t.Fatalf("wrong future partition: %v / %v", rep.ForecastByMonth, rep.ReservationsByMonth)
			}
			if sumForecastMonths(rep.ForecastByMonth[1:]) != 0 {
				t.Fatal("continued past inclusive end")
			}
		})
	}
}

func TestDashboardForecastAgreementOwnershipAndExtraCharges(t *testing.T) {
	t.Parallel()
	now := forecastDate("2026-01-15")
	data := forecastFixture()
	data.Agreements[1] = []models.FlatRatePeriod{{
		ID: 1, PersonID: 1, Amount: 310, Period: models.BillingMonthly,
		StartDate: forecastDate("2026-01-01"), EndDate: forecastDatePtr("2026-01-31"),
		VehicleIDs: []int64{}, // Person-wide, including the reservation below.
	}}
	data.Vehicles = []models.Vehicle{
		{ID: 1, PersonID: 1, Rate: 999, BillingPeriod: models.BillingMonthly,
			StartDate: forecastDate("2026-01-01"), Status: models.StatusStored},
		{ID: 2, PersonID: 1, Rate: 999, BillingPeriod: models.BillingMonthly,
			StartDate: forecastDate("2026-01-01"), Status: models.StatusReserved,
			ReservedFrom: forecastDatePtr("2026-02-01")},
	}
	data.Recurring = []models.FlatRatePeriod{{
		Amount: 31, Period: models.BillingMonthly, StartDate: forecastDate("2026-01-01"),
		EndDate: forecastDatePtr("2026-01-31"),
	}}
	data.Charges = []dashboardForecastCharge{
		{Amount: 5, On: forecastDate("2026-01-15")},
		{Amount: 7, On: forecastDate("2026-01-16")},
	}
	before, _ := json.Marshal(data)
	rep := buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
	if sumForecastMonths(rep.ActualByMonth) != 170 || sumForecastMonths(rep.ForecastByMonth) != 183 {
		t.Fatalf("agreement/extra double counting or cutoff failure: %+v", rep)
	}
	if sumForecastMonths(rep.ReservationsByMonth) != 0 || rep.ForecastByMonth[1] != 0 {
		t.Fatal("agreement coverage invented additional rent")
	}
	joined := strings.Join(rep.Warnings, " ")
	if !strings.Contains(joined, "Reservierungsdaten") || !strings.Contains(joined, "Einzelmiete") {
		t.Fatalf("missing coverage/date warnings: %v", rep.Warnings)
	}
	after, _ := json.Marshal(data)
	if string(before) != string(after) {
		t.Fatal("forecast changed its source data")
	}
}

func TestDashboardForecastProrationSplitAndMonthReconciliation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		period string
		amount float64
		start  string
		end    string
		asOf   string
	}{
		{name: "one cent leap year", period: models.BillingYearly, amount: .01,
			start: "2024-01-01", end: "2024-12-31", asOf: "2024-07-01"},
		{name: "odd cents leap year", period: models.BillingYearly, amount: 13.37,
			start: "2024-01-01", end: "2024-12-31", asOf: "2024-02-29"},
		{name: "one cent february half", period: models.BillingMonthly, amount: .01,
			start: "2026-02-01", end: "2026-02-28", asOf: "2026-02-14"},
		{name: "tiny late reservation", period: models.BillingYearly, amount: .02,
			start: "2026-08-01", end: "2026-12-31", asOf: "2026-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := forecastDate(tc.asOf)
			data := forecastFixture()
			vehicle := models.Vehicle{ID: 1, PersonID: 1, Rate: tc.amount, BillingPeriod: tc.period,
				StartDate: forecastDate(tc.start), EndDate: forecastDatePtr(tc.end), Status: models.StatusStored}
			data.Vehicles = []models.Vehicle{vehicle}
			ys := forecastTestYears(data, now)
			rep := buildDashboardForecast(data, ys, now.Year(), now)
			full := vehicle.CostInRange(models.Category{}, vehicle.StartDate, models.DayAfter(*vehicle.EndDate))
			combined := round2(sumForecastMonths(rep.ActualByMonth) + sumForecastMonths(rep.ForecastByMonth))
			if combined != full {
				t.Fatalf("split invented cents: actual=%v future=%v full=%.2f", rep.ActualByMonth, rep.ForecastByMonth, full)
			}
			for m := range 12 {
				if rep.ForecastByMonth[m] < 0 {
					t.Fatalf("negative future month %d: %v", m+1, rep.ForecastByMonth)
				}
				if m < int(now.Month())-1 && rep.ForecastByMonth[m] != 0 {
					t.Fatalf("future residual leaked into elapsed month %d", m+1)
				}
				if m < int(vehicle.StartDate.Month())-1 && rep.ForecastByMonth[m] != 0 {
					t.Fatalf("future residual leaked before rental month %d", m+1)
				}
			}
			data.Vehicles[0].Status = models.StatusReserved
			reservation := buildDashboardForecast(data, ys, now.Year(), now)
			if sumForecastMonths(reservation.ReservationsByMonth) != sumForecastMonths(rep.ForecastByMonth) {
				t.Fatal("reservation remainder uses different proration")
			}
		})
	}
}

func TestDashboardForecastHistoricalParityAndNextYear(t *testing.T) {
	t.Parallel()
	now := forecastDate("2026-10-09")
	data := forecastFixture()
	data.Vehicles = []models.Vehicle{{
		ID: 1, PersonID: 1, Rate: 100, BillingPeriod: models.BillingYearly,
		StartDate: forecastDate("2024-12-31"), Status: models.StatusStored,
	}}
	data.Agreements[2] = []models.FlatRatePeriod{{
		ID: 2, PersonID: 2, Amount: 15.99, Period: models.BillingMonthly,
		StartDate: forecastDate("2025-01-10"),
	}}
	data.Recurring = []models.FlatRatePeriod{{
		Amount: 22.11, Period: models.BillingYearly, StartDate: forecastDate("2025-05-01"),
	}}
	data.Charges = []dashboardForecastCharge{{Amount: 12.34, On: forecastDate("2026-06-10")}}
	start := forecastDate("2026-01-01")
	until := models.DayAfter(now)
	want := personRent(nil, data.Vehicles, data.Categories, start, until) +
		personRent(data.Agreements[2], nil, data.Categories, start, until) +
		data.Recurring[0].CostInRange(start, until) + 12.34
	ys := forecastTestYears(data, now)
	rep := buildDashboardForecast(data, ys, 2026, now)
	if sumForecastMonths(rep.ActualByMonth) != round2(want) {
		t.Fatalf("actual differs from existing overview composition: %.2f != %.2f", sumForecastMonths(rep.ActualByMonth), want)
	}
	for _, year := range rep.Years {
		selected := buildDashboardForecast(data, ys, year.Year, now)
		if year.Actual != sumForecastMonths(selected.ActualByMonth) ||
			year.Forecast != sumForecastMonths(selected.ForecastByMonth) ||
			year.Reservations != sumForecastMonths(selected.ReservationsByMonth) {
			t.Fatalf("year/month totals differ: %+v", year)
		}
		if year.Year < now.Year() && (year.Forecast != 0 || year.Reservations != 0) {
			t.Fatal("forecast leaked into past")
		}
	}
	next := buildDashboardForecast(data, ys, 2027, now)
	if sumForecastMonths(next.ActualByMonth) != 0 || next.PreviousByMonth != nil || !next.HasYearData {
		t.Fatalf("next-year plan invented actual data or a complete prior year: %+v", next)
	}
	if !slices.Contains(next.ForecastYears, 2027) || slices.Contains(next.AvailableYears, 2027) {
		t.Fatal("mixed planning years into historical navigation")
	}
	if _, err := json.Marshal(next); err != nil {
		t.Fatalf("non-renderable JSON: %v", err)
	}
}

func TestDashboardForecastResidualDoesNotCreditFreeMonths(t *testing.T) {
	t.Parallel()
	periods := []models.FlatRatePeriod{
		{Amount: 0, Period: models.BillingYearly, StartDate: forecastDate("2026-01-01"),
			EndDate: forecastDatePtr("2026-06-30")},
		{Amount: .01, Period: models.BillingYearly, StartDate: forecastDate("2026-07-01"),
			EndDate: forecastDatePtr("2026-12-31")},
	}
	months := forecastMonths(periods, nil, 2026, forecastDate("2026-01-01"), forecastDate("2027-01-01"))
	if sumForecastMonths(months) != .01 || sumForecastMonths(months[:6]) != 0 {
		t.Fatalf("priced residual credited a free-only month: %v", months)
	}
}

func TestDashboardForecastRemaindersStayWithTheirSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		juneAmount  float64
		marchCharge float64
	}{
		{name: "tiny later contract", juneAmount: .01},
		{name: "normal later contract", juneAmount: 30},
		{name: "real negative period", juneAmount: -.01},
		{name: "real negative charge", juneAmount: .01, marchCharge: -7.50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			periods := make([]models.FlatRatePeriod, 0, 101)
			for range 100 {
				periods = append(periods, models.FlatRatePeriod{
					Amount: .01, Period: models.BillingMonthly,
					StartDate: forecastDate("2010-02-01"), EndDate: forecastDatePtr("2010-02-28"),
				})
			}
			periods = append(periods, models.FlatRatePeriod{
				Amount: tc.juneAmount, Period: models.BillingMonthly,
				StartDate: forecastDate("2010-06-01"), EndDate: forecastDatePtr("2010-06-30"),
			})
			charges := []dashboardForecastCharge{{Amount: tc.marchCharge, On: forecastDate("2010-03-01")}}
			start, cutoff, end := forecastDate("2010-01-01"), forecastDate("2010-02-15"), forecastDate("2011-01-01")
			actual := forecastMonths(periods, charges, 2010, start, cutoff)
			future := forecastMonths(periods, charges, 2010, cutoff, end)
			if actual[1] != 1 || sumForecastMonths(actual) != 1 {
				t.Fatalf("accrued contract rounding changed: %v", actual)
			}
			for month, amount := range future {
				want := 0.0
				switch month {
				case 2:
					want = tc.marchCharge
				case 5:
					want = tc.juneAmount
				}
				if amount != want {
					t.Errorf("future month %d = %.2f, want %.2f", month+1, amount, want)
				}
			}
			if got := round2(sumForecastMonths(actual) + sumForecastMonths(future)); got != float64(forecastRangeCents(periods, charges, start, end))/100 {
				t.Errorf("split changed the annual total: %.2f", got)
			}
		})
	}
}

func TestDashboardForecastMonthsAreSourceAdditive(t *testing.T) {
	t.Parallel()
	periods := []models.FlatRatePeriod{
		{Amount: .01, Period: models.BillingMonthly, StartDate: forecastDate("2024-02-01"),
			EndDate: forecastDatePtr("2024-02-29")},
		{Amount: 13.37, Period: models.BillingYearly, StartDate: forecastDate("2023-11-15"),
			EndDate: forecastDatePtr("2024-10-20")},
		{Amount: -.03, Period: models.BillingYearly, StartDate: forecastDate("2024-04-01"),
			EndDate: forecastDatePtr("2024-12-31")},
		{Amount: 0, Period: models.BillingMonthly, StartDate: forecastDate("2024-01-01")},
	}
	charges := []dashboardForecastCharge{
		{Amount: -2.17, On: forecastDate("2024-03-10")},
		{Amount: 3.19, On: forecastDate("2024-11-20")},
	}
	for _, tc := range []struct {
		name string
		from string
		to   string
	}{
		{name: "full leap year", from: "2024-01-01", to: "2025-01-01"},
		{name: "actual to cutoff", from: "2024-01-01", to: "2024-02-15"},
		{name: "future from cutoff", from: "2024-02-15", to: "2025-01-01"},
		{name: "partial final month", from: "2024-02-15", to: "2024-08-12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := forecastDate(tc.from), forecastDate(tc.to)
			combined := forecastMonths(periods, charges, 2024, from, to)
			var separate [12]int64
			for _, period := range periods {
				amounts := forecastMonths([]models.FlatRatePeriod{period}, nil, 2024, from, to)
				for month, amount := range amounts {
					separate[month] += int64(math.Round(amount * 100))
				}
			}
			for _, charge := range charges {
				if !charge.On.Before(from) && charge.On.Before(to) {
					separate[int(charge.On.Month())-1] += int64(math.Round(charge.Amount * 100))
				}
			}
			for month, amount := range combined {
				if want := float64(separate[month]) / 100; amount != want {
					t.Errorf("month %d changed when unrelated sources were combined: %.2f, want %.2f",
						month+1, amount, want)
				}
			}
		})
	}
}

func TestDashboardForecastCutoffPreservesDatedCredits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		asOf       string
		wantActual float64
		wantFuture float64
	}{
		{name: "before half-cent threshold", asOf: "2026-03-15", wantActual: -7.50, wantFuture: 1.01},
		{name: "after half-cent threshold", asOf: "2026-03-16", wantActual: -9.49, wantFuture: 3},
		{name: "last day of month", asOf: "2026-03-31", wantActual: -6.49},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := forecastDate(tc.asOf)
			data := forecastFixture()
			data.Vehicles = []models.Vehicle{{
				ID: 1, PersonID: 1, Rate: .01, BillingPeriod: models.BillingMonthly,
				StartDate: forecastDate("2026-03-01"), EndDate: forecastDatePtr("2026-03-31"),
				Status: models.StatusStored,
			}}
			data.Charges = []dashboardForecastCharge{
				{Amount: -7.50, On: forecastDate("2026-03-15")},
				{Amount: -2, On: forecastDate("2026-03-16")},
				{Amount: 3, On: forecastDate("2026-03-31")},
			}
			rep := buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
			for month := range 12 {
				wantActual, wantFuture := 0.0, 0.0
				if month == 2 {
					wantActual, wantFuture = tc.wantActual, tc.wantFuture
				}
				if rep.ActualByMonth[month] != wantActual || rep.ForecastByMonth[month] != wantFuture {
					t.Errorf("month %d: actual %.2f / future %.2f, want %.2f / %.2f",
						month+1, rep.ActualByMonth[month], rep.ForecastByMonth[month], wantActual, wantFuture)
				}
			}
		})
	}
}

func TestDashboardYearMetadataCacheRevisionDayAndTTL(t *testing.T) {
	t.Parallel()
	h := New(nil)
	now := time.Now()
	ys := newDashboardYears(now)
	ys.Present[now.Year()-2] = true
	ys.finish(now.Year())
	h.storeOverview(now.Year(), now.Format(dateLayout), 7, overviewCachedResponse{years: ys})
	for _, tc := range []struct {
		name     string
		day      string
		revision int64
		at       time.Time
		want     bool
	}{
		{name: "fresh", day: now.Format(dateLayout), revision: 7, at: now, want: true},
		{name: "write", day: now.Format(dateLayout), revision: 8, at: now},
		{name: "next day", day: now.AddDate(0, 0, 1).Format(dateLayout), revision: 7, at: now},
		{name: "ttl", day: now.Format(dateLayout), revision: 7, at: now.Add(2 * overviewCacheTTL)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := h.cachedOverviewYears(tc.day, tc.revision, tc.at)
			if ok != tc.want || (ok && !reflect.DeepEqual(got.Available, ys.Available)) {
				t.Fatalf("cache validity %v: %+v", ok, got)
			}
		})
	}
}

func TestDashboardForecastReadFailureCleansCanceledSnapshot(t *testing.T) {
	t.Parallel()
	readErr := errors.New("forecast read failed")
	tx := &taxSnapshotFailureTx{queryErr: readErr}
	db := &taxSnapshotFailureDB{tx: tx}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := readDashboardForecast(ctx, db, 2026, forecastDate("2026-10-09"))
	if !errors.Is(err, readErr) || !tx.isRolledBack || tx.rollbackContextErr != nil {
		t.Fatalf("failed snapshot leaked or lost its cause: %v / %+v", err, tx)
	}
	if db.options.IsoLevel != pgx.RepeatableRead || db.options.AccessMode != pgx.ReadOnly {
		t.Fatalf("unsafe forecast snapshot: %+v", db.options)
	}
}

func TestDashboardYearMetadataAgeDoesNotRenewAcrossSummaries(t *testing.T) {
	t.Parallel()
	h := New(nil)
	now := time.Now()
	ys := newDashboardYears(now)
	ys.loadedAt = now.Add(-overviewCacheTTL - time.Second)
	// A new total for another year may still carry the old metadata. Neither
	// metadata nor this fresh summary may extend that metadata's original TTL.
	h.storeOverview(now.Year(), now.Format(dateLayout), 7, overviewCachedResponse{years: ys})
	if _, ok := h.cachedOverviewYears(now.Format(dateLayout), 7, now); ok {
		t.Fatal("new summary renewed stale year metadata")
	}
	if _, ok := h.cachedOverview(now.Year(), now.Format(dateLayout), 7, now); ok {
		t.Fatal("fresh summary returned stale year metadata")
	}
}

func TestDashboardForecastBusinessDayCutoff(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("business", 2*60*60)
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, zone) // Still prior UTC year.
	data := forecastFixture()
	data.Vehicles = []models.Vehicle{{
		ID: 1, PersonID: 1, Rate: 31, BillingPeriod: models.BillingMonthly,
		StartDate: forecastDate("2026-01-01"), EndDate: forecastDatePtr("2026-01-31"), Status: models.StatusStored,
	}}
	rep := buildDashboardForecast(data, forecastTestYears(data, now), 2026, now)
	if rep.CurrentYear != 2026 || rep.AsOf != "2026-01-01" || rep.ActualByMonth[0] != 1 || rep.ForecastByMonth[0] != 30 {
		t.Fatalf("used UTC timestamp instead of business calendar day: %+v", rep)
	}
	for _, values := range [][]float64{rep.ActualByMonth, rep.ForecastByMonth, rep.ReservationsByMonth} {
		for _, amount := range values {
			if math.IsNaN(amount) || math.IsInf(amount, 0) {
				t.Fatal("non-finite amount")
			}
		}
	}
}
