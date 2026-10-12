//go:build integration

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/models"
)

func TestTariffDuplicateUsesOneConsistentSourceSnapshot(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	newCategory := tariffCategory(t, h, 120, 1200)
	source := tariffCreateVehicle(t, h, map[string]any{
		"person_id": pid, "category_id": category.ID, "label": "before", "rate": 85,
		"start_date": "2098-01-01",
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The copy has read its price before its INSERT waits for the category FK.
	// A simultaneous source edit must not mix newer master data with that price.
	if _, err := tx.Exec(ctx, `SELECT id FROM categories WHERE id=$1 FOR UPDATE`, category.ID); err != nil {
		t.Fatal(err)
	}
	req := tariffJSON(t, http.MethodPost, "/vehicles/duplicate", map[string]any{"start_date": "2099-01-01"}).WithContext(ctx)
	req.SetPathValue("id", strconv.FormatInt(source.ID, 10))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.DuplicateVehicle(rec, req)
		done <- rec
	}()
	waitForLockWait(ctx, t, h, "%INSERT INTO vehicles%")
	edit := tariffJSON(t, http.MethodPut, "/vehicles", map[string]any{
		"person_id": pid, "category_id": newCategory.ID, "label": "after", "rate": 120,
		"start_date": "2098-01-01",
	}).WithContext(ctx)
	edit.SetPathValue("id", strconv.FormatInt(source.ID, 10))
	edited := httptest.NewRecorder()
	h.UpdateVehicle(edited, edit)
	if edited.Code != http.StatusOK {
		t.Fatalf("concurrent source edit: %d %s", edited.Code, edited.Body.String())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if rec.Code != http.StatusCreated {
			t.Fatalf("copy: %d %s", rec.Code, rec.Body.String())
		}
		var duplicate models.Vehicle
		if err := json.Unmarshal(rec.Body.Bytes(), &duplicate); err != nil {
			t.Fatal(err)
		}
		if duplicate.Rate != 85 || duplicate.CategoryID != category.ID || duplicate.Label != "before" {
			t.Fatalf("copy combined inconsistent source snapshots: %+v", duplicate)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestTariffDuplicateCapturesLegacyPrice(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	category := tariffCategory(t, h, 85, 850)
	for _, tc := range []struct {
		name     string
		override *float64
		want     float64
	}{
		{name: "inherited", want: 85},
		{name: "individual", override: vehicleRatePtr(42), want: 42},
		{name: "free", override: vehicleRatePtr(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id int64
			if err := h.Pool.QueryRow(t.Context(),
				`INSERT INTO vehicles(person_id,category_id,rate,cost_override,start_date,status)
				 VALUES($1,$2,0,$3,'2098-01-01','reserved') RETURNING id`, pid, category.ID, tc.override).Scan(&id); err != nil {
				t.Fatal(err)
			}
			req := tariffJSON(t, http.MethodPost, "/vehicles/duplicate", map[string]any{"start_date": "2099-01-01"})
			req.SetPathValue("id", strconv.FormatInt(id, 10))
			rec := httptest.NewRecorder()
			h.DuplicateVehicle(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
			}
			var duplicate models.Vehicle
			if err := json.Unmarshal(rec.Body.Bytes(), &duplicate); err != nil {
				t.Fatal(err)
			}
			if duplicate.Rate != tc.want || duplicate.EffectiveRate != tc.want ||
				(tc.want == 0 && duplicate.CostOverride == nil) {
				t.Fatalf("copy did not fix agreed price: %+v", duplicate)
			}
			if original := tariffReadVehicle(t, h, id); original.Rate != 0 || original.EffectiveRate != tc.want {
				t.Fatalf("source changed while duplicating: %+v", original)
			}
		})
	}
}

func TestTariffVehicleEditPreservesExistingPrice(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	oldCategory := tariffCategory(t, h, 85, 850)
	newCategory := tariffCategory(t, h, 120, 1200)
	for _, tc := range []struct {
		name     string
		rate     float64
		override *float64
		want     float64
		explicit *float64
	}{
		{name: "legacy inherited", want: 85},
		{name: "legacy individual", override: vehicleRatePtr(42), want: 42},
		{name: "legacy free", override: vehicleRatePtr(0)},
		{name: "fixed rate", rate: 70, want: 70},
		{name: "explicit free correction", rate: 70, explicit: vehicleRatePtr(0)},
		{name: "explicit negotiated correction", rate: 70, explicit: vehicleRatePtr(50), want: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id int64
			if err := h.Pool.QueryRow(t.Context(),
				`INSERT INTO vehicles(person_id,category_id,rate,cost_override,start_date,status)
				 VALUES($1,$2,$3,$4,'2098-01-01','reserved') RETURNING id`,
				pid, oldCategory.ID, tc.rate, tc.override).Scan(&id); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{
				"person_id": pid, "category_id": newCategory.ID, "start_date": "2098-01-01",
				"status": "reserved", "rate": tc.explicit, "cost_override": nil,
			}
			req := tariffJSON(t, http.MethodPut, "/vehicles", body)
			req.SetPathValue("id", strconv.FormatInt(id, 10))
			rec := httptest.NewRecorder()
			h.UpdateVehicle(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
			}
			got := tariffReadVehicle(t, h, id)
			if got.CategoryID != newCategory.ID || got.Rate != tc.want || got.EffectiveRate != tc.want ||
				(tc.want == 0 && got.CostOverride == nil) {
				t.Fatalf("edit lost agreed price: %+v", got)
			}
		})
	}
	// After each explicit edit fixed its own price, both categories can change
	// without a backfill or a change to any existing booking.
	for _, category := range []models.Category{oldCategory, newCategory} {
		rec := httptest.NewRecorder()
		h.UpdateCategory(rec, tariffUpdateRequest(t, category, 130, 1300))
		if rec.Code != http.StatusOK {
			t.Fatalf("confirmed prices still blocked tariff: %d %s", rec.Code, rec.Body.String())
		}
	}
}
