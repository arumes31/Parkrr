package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// lockFinancialSourceTx takes the issuer's person lock before source row locks.
// A concurrent ownership change is rejected rather than using the wrong lock.
func lockFinancialSourceTx(ctx context.Context, tx pgx.Tx, kind string, id, newPersonID int64) error {
	table, ok := map[string]string{
		"vehicle": "vehicles", "charge": "charges", "agreement": "flat_rate_periods", "recurring": "recurring_charges",
	}[kind]
	if !ok {
		return fmt.Errorf("unsupported financial source %q", kind)
	}
	// Security: the table is selected exclusively from the fixed allowlist above.
	query := `SELECT person_id FROM ` + table + ` WHERE id=$1`
	var personID int64
	if err := tx.QueryRow(ctx, query, id).Scan(&personID); err != nil {
		return err
	}
	first, second := personID, newPersonID
	if second > 0 && second < first {
		first, second = second, first
	}
	if err := lockInvoicePersonTx(ctx, tx, first); err != nil {
		return err
	}
	if second > 0 && second != first {
		if err := lockInvoicePersonTx(ctx, tx, second); err != nil {
			return err
		}
	}
	var lockedPersonID int64
	if err := tx.QueryRow(ctx, query+` FOR UPDATE`, id).Scan(&lockedPersonID); err != nil {
		return err
	}
	if lockedPersonID != personID {
		return &settlementConflictError{msg: "Kundenzuordnung wurde geändert – bitte neu laden"}
	}
	return nil
}

func lockFinancialSource(w http.ResponseWriter, r *http.Request, tx pgx.Tx, kind string, id, newPersonID int64) bool {
	err := lockFinancialSourceTx(r.Context(), tx, kind, id, newPersonID)
	if err == nil {
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Eintrag nicht gefunden")
	} else if !writeSettlementConflict(w, err) {
		serverError(w, r, "Eintrag konnte nicht gesperrt werden", err)
	}
	return false
}
