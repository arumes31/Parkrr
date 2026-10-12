package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeletePaymentTransactionConflictResponse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{
			name:       "deadlock",
			err:        &pgconn.PgError{Code: "40P01"},
			wantStatus: http.StatusConflict,
			wantError:  "Gleichzeitige Änderung an dieser Zahlung – bitte erneut versuchen",
		},
		{
			name:       "wrapped serialization failure",
			err:        fmt.Errorf("transaction: %w", &pgconn.PgError{Code: "40001"}),
			wantStatus: http.StatusConflict,
			wantError:  "Gleichzeitige Änderung an dieser Zahlung – bitte erneut versuchen",
		},
		{
			name:       "charge claim conflict",
			err:        &pgconn.PgError{Code: "23505", ConstraintName: "charge_claim_exclusive"},
			wantStatus: http.StatusConflict,
			wantError:  "Position wurde zwischenzeitlich bezahlt oder fakturiert – bitte neu laden.",
		},
		{
			name:       "other transaction error",
			err:        errors.New("connection unavailable"),
			wantStatus: http.StatusInternalServerError,
			wantError:  "could not delete payment",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cfg, err := pgxpool.ParseConfig(
				"postgres://unused:unused@127.0.0.1/unused?sslmode=disable&pool_max_conns=1&pool_min_conns=0",
			)
			if err != nil {
				t.Fatal(err)
			}
			// Fail before dialing: exercise the handler's transaction error path
			// without connecting to any database or changing financial records.
			cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
				return tc.err
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()

			req := httptest.NewRequest(http.MethodDelete, "/api/payments/1", nil).WithContext(ctx)
			req.SetPathValue("id", "1")
			rec := httptest.NewRecorder()
			h := &Handler{Pool: pool}
			h.DeletePayment(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var got struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error != tc.wantError {
				t.Errorf("error = %q, want %q", got.Error, tc.wantError)
			}
		})
	}
}
