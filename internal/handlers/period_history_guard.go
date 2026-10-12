package handlers

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const periodPaymentHistoryMessage = "Zu diesem Zeitraum besteht bereits Zahlungshistorie. " +
	"Betrag, Zahlungsstatus oder Quelle können derzeit nicht zurückgesetzt oder ersetzt werden. " +
	"Die bestehende Zahlung bleibt unverändert. Für eine Korrektur ist ein eigener Storno-/Neubuchungsablauf erforderlich."

// preservePeriodPaymentHistoryTx is the interim safety boundary for period
// sliders. Until a replacement-aware reversal workflow exists, neither active
// nor reversed receipts may be removed to reset or replace a settlement.
// Callers hold the person's settlement lock and roll back all flag changes on
// conflict. Existing tax-year locks retain their more specific response.
func preservePeriodPaymentHistoryTx(ctx context.Context, tx pgx.Tx, kind string, refID int64, periods ...string) error {
	if err := protectPeriodPaymentsTx(ctx, tx, kind, refID, periods...); err != nil {
		return err
	}
	var exists bool
	// Imported/restored rows can carry a settlement key without auto=true.
	// The uniqueness constraint includes them too; never erase or replace them.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payments
		WHERE settles_kind=$1 AND settles_ref=$2
		AND ($3::text[] IS NULL OR settles_period=ANY($3)))`, kind, refID, periods).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return &settlementConflictError{msg: periodPaymentHistoryMessage}
	}
	return nil
}
