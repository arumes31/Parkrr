//go:build integration

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/models"
)

func tariffCategory(t *testing.T, h *Handler, monthly, yearly float64) models.Category {
	t.Helper()
	rec := httptest.NewRecorder()
	h.CreateCategory(rec, tariffJSON(t, http.MethodPost, "/categories", map[string]any{
		"name":                 "R24-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		"default_monthly_cost": monthly, "default_yearly_cost": yearly,
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("category: %d %s", rec.Code, rec.Body.String())
	}
	var category models.Category
	if err := json.Unmarshal(rec.Body.Bytes(), &category); err != nil {
		t.Fatal(err)
	}
	return category
}

func tariffJSON(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(method, target, bytes.NewReader(data))
}

func tariffUpdateRequest(t *testing.T, category models.Category, monthly, yearly float64) *http.Request {
	t.Helper()
	req := tariffJSON(t, http.MethodPut, "/categories", map[string]any{
		"name": category.Name, "default_monthly_cost": monthly, "default_yearly_cost": yearly,
	})
	req.SetPathValue("id", strconv.FormatInt(category.ID, 10))
	return req
}

func tariffCreateVehicle(t *testing.T, h *Handler, body map[string]any) models.Vehicle {
	t.Helper()
	rec := httptest.NewRecorder()
	h.CreateVehicle(rec, tariffJSON(t, http.MethodPost, "/vehicles", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create vehicle: %d %s", rec.Code, rec.Body.String())
	}
	var vehicle models.Vehicle
	if err := json.Unmarshal(rec.Body.Bytes(), &vehicle); err != nil {
		t.Fatal(err)
	}
	return vehicle
}

func tariffReadVehicle(t *testing.T, h *Handler, id int64) models.Vehicle {
	t.Helper()
	rec := httptest.NewRecorder()
	h.writeVehicle(rec, t.Context(), id, http.StatusOK)
	if rec.Code != http.StatusOK {
		t.Fatalf("read vehicle: %d %s", rec.Code, rec.Body.String())
	}
	var vehicle models.Vehicle
	if err := json.Unmarshal(rec.Body.Bytes(), &vehicle); err != nil {
		t.Fatal(err)
	}
	return vehicle
}

func TestTariffUpdateAffectsOnlyNewBookings(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	bookings := []models.Vehicle{}
	for _, tc := range []struct {
		name   string
		period string
		start  string
		status string
		rate   *float64
	}{
		{name: "existing monthly", period: "monthly", start: "2024-01-31", status: "stored"},
		{name: "existing yearly", period: "yearly", start: "2024-02-29", status: "stored"},
		{name: "future reservation", period: "monthly", start: "2098-01-01", status: "reserved"},
		{name: "future yearly", period: "yearly", start: "2098-01-01", status: "stored"},
		{name: "individual price", period: "monthly", start: "2025-01-01", status: "stored", rate: vehicleRatePtr(42)},
		{name: "free booking", period: "monthly", start: "2025-01-01", status: "stored", rate: vehicleRatePtr(0)},
	} {
		body := map[string]any{
			"person_id": pid, "category_id": category.ID, "billing_period": tc.period,
			"label": tc.name, "start_date": tc.start, "status": tc.status,
		}
		if tc.rate != nil {
			body["rate"] = *tc.rate
		}
		bookings = append(bookings, tariffCreateVehicle(t, h, body))
	}
	rec := httptest.NewRecorder()
	h.UpdateCategory(rec, tariffUpdateRequest(t, category, 90, 900))
	if rec.Code != http.StatusOK {
		t.Fatalf("update tariff: %d %s", rec.Code, rec.Body.String())
	}
	for _, before := range bookings {
		after := tariffReadVehicle(t, h, before.ID)
		if after.Rate != before.Rate || after.EffectiveRate != before.EffectiveRate {
			t.Errorf("%s repriced: before=%v/%v after=%v/%v",
				before.Label, before.Rate, before.EffectiveRate, after.Rate, after.EffectiveRate)
		}
	}
	for _, tc := range []struct {
		period string
		want   float64
	}{{period: "monthly", want: 90}, {period: "yearly", want: 900}} {
		newBooking := tariffCreateVehicle(t, h, map[string]any{
			"person_id": pid, "category_id": category.ID, "billing_period": tc.period,
			"start_date": "2098-01-01", "status": "reserved",
		})
		if newBooking.Rate != tc.want || newBooking.EffectiveRate != tc.want {
			t.Errorf("new %s booking did not capture current tariff: %+v", tc.period, newBooking)
		}
	}
}

func TestTariffUpdateGuardsLegacyPricesWithoutBackfill(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	var legacyID int64
	if err := h.Pool.QueryRow(t.Context(),
		`INSERT INTO vehicles(person_id,category_id,billing_period,rate,start_date,status,archived)
		 VALUES($1,$2,'monthly',0,'2098-01-01','reserved',true) RETURNING id`, pid, category.ID).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	category.Name = "renamed-" + category.Name
	for _, tc := range []struct {
		name    string
		monthly float64
		yearly  float64
		want    int
	}{
		{name: "same price rename is allowed", monthly: 85, yearly: 850, want: http.StatusOK},
		{name: "yearly change does not affect monthly legacy", monthly: 85, yearly: 900, want: http.StatusOK},
		{name: "same effective price is allowed", monthly: 85.001, yearly: 900, want: http.StatusOK},
		{name: "monthly price change rejected", monthly: 90, yearly: 900, want: http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.UpdateCategory(rec, tariffUpdateRequest(t, category, tc.monthly, tc.yearly))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusConflict && !strings.Contains(rec.Body.String(), "1 bestehende Buchung") {
				t.Fatalf("missing actionable legacy count: %s", rec.Body.String())
			}
		})
	}
	var rate, monthly float64
	var override *float64
	if err := h.Pool.QueryRow(t.Context(),
		`SELECT v.rate,v.cost_override,c.default_monthly_cost FROM vehicles v JOIN categories c ON c.id=v.category_id WHERE v.id=$1`,
		legacyID).Scan(&rate, &override, &monthly); err != nil {
		t.Fatal(err)
	}
	if rate != 0 || override != nil || monthly != 85 {
		t.Fatalf("legacy or tariff was silently rewritten: rate=%v override=%v monthly=%v", rate, override, monthly)
	}
}

func TestTariffCreateWaitsForCommittedPrice(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx,
		`UPDATE categories SET default_monthly_cost=90 WHERE id=$1`, category.ID); err != nil {
		t.Fatal(err)
	}
	req := tariffJSON(t, http.MethodPost, "/vehicles", map[string]any{
		"person_id": pid, "category_id": category.ID, "start_date": "2098-01-01",
	}).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.CreateVehicle(rec, req)
		done <- rec
	}()
	waitForLockWait(ctx, t, h, "%FROM categories WHERE id=$1 FOR SHARE%")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if rec.Code != http.StatusCreated {
			t.Fatalf("waiting booking: %d %s", rec.Code, rec.Body.String())
		}
		var vehicle models.Vehicle
		if err := json.Unmarshal(rec.Body.Bytes(), &vehicle); err != nil {
			t.Fatal(err)
		}
		if vehicle.Rate != 90 || vehicle.EffectiveRate != 90 {
			t.Fatalf("new booking did not capture committed tariff: %+v", vehicle)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestTariffUpdateAllowsLegacyIndividualAndFreePrices(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 0, 0)
	free := tariffCreateVehicle(t, h, map[string]any{
		"person_id": pid, "category_id": category.ID, "start_date": "2098-01-01",
	})
	var customID int64
	if err := h.Pool.QueryRow(t.Context(),
		`INSERT INTO vehicles(person_id,category_id,rate,cost_override,start_date,status)
		 VALUES($1,$2,0,42,'2098-01-01','stored') RETURNING id`, pid, category.ID).Scan(&customID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.UpdateCategory(rec, tariffUpdateRequest(t, category, 90, 900))
	if rec.Code != http.StatusOK {
		t.Fatalf("individual/free bookings must not block: %d %s", rec.Code, rec.Body.String())
	}
	if got := tariffReadVehicle(t, h, free.ID); got.EffectiveRate != 0 || got.CostOverride == nil {
		t.Fatalf("free default repriced: %+v", got)
	}
	if got := tariffReadVehicle(t, h, customID); got.EffectiveRate != 42 {
		t.Fatalf("legacy individual price changed: %+v", got)
	}
}

func TestTariffCaptureSerializesWithPriceChange(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rate, err := categoryDefaultRate(ctx, tx, category.ID, models.BillingMonthly)
	if err != nil || rate != 85 {
		t.Fatalf("capture initial price: %v %v", rate, err)
	}
	req := tariffUpdateRequest(t, category, 90, 900).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.UpdateCategory(rec, req)
		done <- rec
	}()
	waitForLockWait(ctx, t, h, "%FROM categories WHERE id=$1 FOR UPDATE%")
	created, err := createVehiclesTx(ctx, tx, pid, models.BillingMonthly,
		time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC), []newVehicleReq{{CategoryID: category.ID}})
	if err != nil || len(created) != 1 {
		t.Fatalf("create inline booking: %v %v", created, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("waiting tariff update: %d %s", rec.Code, rec.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got := tariffReadVehicle(t, h, created[0].id); got.EffectiveRate != 85 {
		t.Fatalf("already captured price changed: %+v", got)
	}
}
