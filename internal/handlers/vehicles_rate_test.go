package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/models"
)

func vehicleRatePtr(value float64) *float64 { return &value }

func TestPersistedCostOverride(t *testing.T) {
	tests := []struct {
		name     string
		req      vehicleRequest
		existing *float64
		want     *float64
	}{
		{name: "explicit zero gets a free-booking marker", req: vehicleRequest{Rate: vehicleRatePtr(0)}, want: vehicleRatePtr(0)},
		{name: "positive locked rate needs no marker", req: vehicleRequest{Rate: vehicleRatePtr(85)}, existing: vehicleRatePtr(42), want: nil},
		{name: "legacy override request is retained", req: vehicleRequest{CostOverride: vehicleRatePtr(42)}, want: vehicleRatePtr(42)},
		{name: "omitted billing fields preserve existing override", req: vehicleRequest{}, existing: vehicleRatePtr(42), want: vehicleRatePtr(42)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := persistedCostOverride(tt.req, tt.existing)
			if got == nil || tt.want == nil {
				if got != tt.want {
					t.Fatalf("persistedCostOverride() = %v, want %v", got, tt.want)
				}
				return
			}
			if *got != *tt.want {
				t.Errorf("persistedCostOverride() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func TestVehicleZeroRateCompatibilityAndFreeze(t *testing.T) {
	h := testHandler(t)
	compliantSeller(t, h)
	pid := createIntegrationPerson(t, h)
	ctx := context.Background()
	categoryName := "Zero-Rate-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var categoryID int64
	if err := h.Pool.QueryRow(ctx,
		`INSERT INTO categories (name, default_monthly_cost, default_yearly_cost)
		 VALUES ($1,85,1020) RETURNING id`, categoryName).Scan(&categoryID); err != nil {
		t.Fatalf("create category: %v", err)
	}

	// A new explicit zero is marked as free, survives API enrichment, and keeps
	// that marker when duplicated for another storage period.
	createBody, _ := json.Marshal(map[string]any{
		"person_id": pid, "category_id": categoryID, "billing_period": "monthly",
		"rate": 0, "start_date": "2026-01-01", "status": "stored",
	})
	createRec := httptest.NewRecorder()
	h.CreateVehicle(createRec, httptest.NewRequest(http.MethodPost, "/api/vehicles", bytes.NewReader(createBody)))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create free vehicle: %d %s", createRec.Code, createRec.Body.String())
	}
	var free models.Vehicle
	if err := json.Unmarshal(createRec.Body.Bytes(), &free); err != nil {
		t.Fatalf("decode free vehicle: %v", err)
	}
	if free.Rate != 0 || free.CostOverride == nil || *free.CostOverride != 0 || free.EffectiveRate != 0 {
		t.Fatalf("free marker not persisted/enriched: %+v", free)
	}

	duplicateBody := bytes.NewBufferString(`{"start_date":"2026-11-01"}`)
	duplicateReq := httptest.NewRequest(http.MethodPost, "/api/vehicles/"+strconv.FormatInt(free.ID, 10)+"/duplicate", duplicateBody)
	duplicateReq.SetPathValue("id", strconv.FormatInt(free.ID, 10))
	duplicateRec := httptest.NewRecorder()
	h.DuplicateVehicle(duplicateRec, duplicateReq)
	if duplicateRec.Code != http.StatusCreated {
		t.Fatalf("duplicate free vehicle: %d %s", duplicateRec.Code, duplicateRec.Body.String())
	}
	var duplicate models.Vehicle
	_ = json.Unmarshal(duplicateRec.Body.Bytes(), &duplicate)
	if duplicate.CostOverride == nil || *duplicate.CostOverride != 0 || duplicate.EffectiveRate != 0 {
		t.Fatalf("duplicate lost free marker: %+v", duplicate)
	}

	// An unmarked legacy zero must continue using the historical Tarif fallback.
	var legacyID, blockedLegacyID int64
	legacyStart := firstOfMonthMonthsAgo(2)
	if err := h.Pool.QueryRow(ctx,
		`INSERT INTO vehicles (person_id, category_id, billing_period, rate, cost_override, start_date, status)
		 VALUES ($1,$2,'monthly',0,NULL,$3,'stored') RETURNING id`,
		pid, categoryID, legacyStart).Scan(&legacyID); err != nil {
		t.Fatalf("create legacy vehicle: %v", err)
	}
	if err := h.Pool.QueryRow(ctx,
		`INSERT INTO vehicles (person_id, category_id, billing_period, rate, cost_override, start_date, status)
		 VALUES ($1,$2,'monthly',0,NULL,$3,'stored') RETURNING id`,
		pid, categoryID, legacyStart).Scan(&blockedLegacyID); err != nil {
		t.Fatalf("create second legacy vehicle: %v", err)
	}
	legacyRec := httptest.NewRecorder()
	h.writeVehicle(legacyRec, ctx, legacyID, http.StatusOK)
	var legacy models.Vehicle
	_ = json.Unmarshal(legacyRec.Body.Bytes(), &legacy)
	if legacy.CostOverride != nil || legacy.EffectiveRate != 85 {
		t.Fatalf("legacy fallback changed: %+v", legacy)
	}

	createInvoice(t, h, pid)
	normalizeBody, _ := json.Marshal(map[string]any{
		"person_id": pid, "category_id": categoryID, "billing_period": "monthly",
		"rate": 85, "start_date": legacyStart.Format("2006-01-02"), "status": "stored",
	})
	normalizeReq := httptest.NewRequest(http.MethodPut, "/api/vehicles/"+strconv.FormatInt(legacyID, 10), bytes.NewReader(normalizeBody))
	normalizeReq.SetPathValue("id", strconv.FormatInt(legacyID, 10))
	normalizeRec := httptest.NewRecorder()
	h.UpdateVehicle(normalizeRec, normalizeReq)
	if normalizeRec.Code != http.StatusOK {
		t.Fatalf("same-price legacy normalization was blocked: %d %s", normalizeRec.Code, normalizeRec.Body.String())
	}

	updateBody, _ := json.Marshal(map[string]any{
		"person_id": pid, "category_id": categoryID, "billing_period": "monthly",
		"rate": 0, "start_date": legacyStart.Format("2006-01-02"), "status": "stored",
	})
	updateReq := httptest.NewRequest(http.MethodPut, "/api/vehicles/"+strconv.FormatInt(blockedLegacyID, 10), bytes.NewReader(updateBody))
	updateReq.SetPathValue("id", strconv.FormatInt(blockedLegacyID, 10))
	updateRec := httptest.NewRecorder()
	h.UpdateVehicle(updateRec, updateReq)
	if updateRec.Code != http.StatusConflict {
		t.Fatalf("invoiced legacy vehicle accepted free marker: %d %s", updateRec.Code, updateRec.Body.String())
	}
}

func TestAgreementInlineZeroTariffPersistsFreeMarker(t *testing.T) {
	h := testHandler(t)
	pid := createIntegrationPerson(t, h)
	ctx := context.Background()
	categoryName := "Agreement-Zero-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var categoryID int64
	if err := h.Pool.QueryRow(ctx,
		`INSERT INTO categories (name, default_monthly_cost, default_yearly_cost)
		 VALUES ($1,0,0) RETURNING id`, categoryName).Scan(&categoryID); err != nil {
		t.Fatalf("create zero category: %v", err)
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := createVehiclesTx(ctx, tx, pid, models.BillingMonthly, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), []newVehicleReq{{CategoryID: categoryID}})
	if err != nil || len(created) != 1 {
		t.Fatalf("create inline zero vehicle: created=%v err=%v", created, err)
	}
	var rate float64
	var override *float64
	if err := tx.QueryRow(ctx, `SELECT rate, cost_override FROM vehicles WHERE id=$1`, created[0].id).Scan(&rate, &override); err != nil {
		t.Fatalf("read inline zero vehicle: %v", err)
	}
	if rate != 0 || override == nil || *override != 0 {
		t.Fatalf("inline zero marker: rate=%v override=%v", rate, override)
	}
}
