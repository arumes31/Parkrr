//go:build integration

package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestChangeVehicleStatusDateValidation(t *testing.T) {
	tests := []struct {
		name string
		date string
		want string
	}{
		{name: "overlong", date: strings.Repeat("2", maxDateLen+1), want: "date is too long"},
		{name: "padded overlong", date: "  " + strings.Repeat("2", maxDateLen+1) + "  ", want: "date is too long"},
		{name: "invalid calendar date", date: "2026-02-30", want: "date must be YYYY-MM-DD"},
		{name: "trimmed valid date", date: " 2026-01-15 "},
	}
	for _, status := range []string{"collected", "cancelled"} {
		for _, tt := range tests {
			t.Run(status+"/"+tt.name, func(t *testing.T) {
				h := testHandler(t)
				id := mkPhotoVehicle(t, h)
				if _, err := h.Pool.Exec(t.Context(),
					`UPDATE vehicles SET start_date='2026-01-01', end_date=NULL WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				rec := changeStatus(t, h, id, map[string]any{"status": status, "date": tt.date})
				gotStatus, end, archived, paid := vehicleRow(t, h, id)
				if tt.want != "" {
					if rec.Code != http.StatusBadRequest {
						t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
					}
					var body struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if body.Error != tt.want {
						t.Errorf("error = %q, want %q", body.Error, tt.want)
					}
					if gotStatus != "stored" || end != nil || archived || paid {
						t.Errorf("rejected date changed vehicle: status=%s end=%v archived=%t paid=%t",
							gotStatus, end, archived, paid)
					}
					return
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
				}
				if gotStatus != status || end == nil || end.Format(dateLayout) != "2026-01-15" {
					t.Errorf("valid date not persisted: status=%s end=%v", gotStatus, end)
				}
			})
		}
	}
}
