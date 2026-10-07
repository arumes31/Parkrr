package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Releasing financial coverage must also restore visibility. Only sources of
// the corrected payment/invoice are considered, never unrelated manual archives.
func (h *Handler) reopenUncoveredVehiclesTx(r *http.Request, tx pgx.Tx, ids []int64) error {
	rows, err := tx.Query(r.Context(), `UPDATE vehicles v SET archived=false,updated_at=now()
		WHERE v.id=ANY($1) AND v.archived AND v.status='collected'
		AND NOT (`+vehicleArchivePredicate+`) RETURNING v.id`, ids, businessDay(h.now()))
	if err != nil {
		return err
	}
	var reopened []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		reopened = append(reopened, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range reopened {
		if err := h.auditChangeTx(r.Context(), tx, r, "update", "vehicle", id,
			"Finanziell wieder geöffnet nach Zahlungskorrektur oder Rechnungsstorno; Stellplatz bleibt frei",
			diffFields(map[string]any{"archived": true}, map[string]any{"archived": false})); err != nil {
			return err
		}
	}
	return nil
}

func invoiceVehicleIDsTx(ctx context.Context, tx pgx.Tx, invoiceIDs []int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ref_id FROM invoice_source WHERE kind='vehicle' AND invoice_id=ANY($1) ORDER BY ref_id`, invoiceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// A billed final partial period cannot be extended in place: its period key is
// already consumed and cannot invoice the additional days a second time.
func (h *Handler) guardInvoicedPartialExtension(ctx context.Context, q rowQuerier, kind string, id int64, oldEnd, newEnd *time.Time, period string) error {
	if oldEnd == nil || (newEnd != nil && !newEnd.After(*oldEnd)) {
		return nil
	}
	key := oldEnd.Format("2006-01")
	lastDay := time.Date(oldEnd.Year(), oldEnd.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	if period == "yearly" {
		key = oldEnd.Format("2006")
		lastDay = time.Date(oldEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}
	if sameTaxDay(*oldEnd, lastDay) {
		return nil
	}
	locked, err := periodInvoicedTx(ctx, q, kind, id, key)
	if err != nil {
		return err
	}
	if locked {
		return &settlementConflictError{msg: "Die abschließende Teilperiode ist bereits fakturiert – zusätzlichen Zeitraum separat anlegen oder Rechnung zuerst stornieren"}
	}
	return nil
}

// vehicleArchivePredicate is shared by single and invoice-payment closure.
// Invoice settlement alone is insufficient: every rental period must be covered,
// and the final service day must be over before it is eligible for invoicing.
// $2 is the business day. Legacy unkeyed invoice sources fail closed here; they
// are not evidence of coverage for an arbitrary subsequently extended rental.
const vehicleArchivePredicate = `v.status='cancelled'
 OR (v.status='collected' AND v.paid AND v.paid_through >= v.end_date)
 OR (v.status='collected' AND v.end_date < $2::date
     AND EXISTS (SELECT 1 FROM invoice_source s JOIN invoices i ON i.id=s.invoice_id
                 WHERE s.kind='vehicle' AND s.ref_id=v.id AND NOT i.canceled)
     AND NOT EXISTS (SELECT 1 FROM invoice_source s JOIN invoices i ON i.id=s.invoice_id
                     WHERE s.kind='vehicle' AND s.ref_id=v.id AND NOT i.canceled
                       AND i.total-i.paid_amount > 0.005)
     AND NOT EXISTS (
         SELECT 1 FROM generate_series(
             date_trunc(CASE WHEN v.billing_period='yearly' THEN 'year' ELSE 'month' END,v.start_date::timestamp),
             v.end_date::timestamp,
             CASE WHEN v.billing_period='yearly' THEN interval '1 year' ELSE interval '1 month' END
         ) AS p(start_day)
         WHERE NOT (v.paid AND COALESCE(v.paid_through >= LEAST(v.end_date,
             (p.start_day + CASE WHEN v.billing_period='yearly' THEN interval '1 year' ELSE interval '1 month' END - interval '1 day')::date),false))
         AND NOT EXISTS (SELECT 1 FROM invoice_source s JOIN invoices i ON i.id=s.invoice_id
             WHERE s.kind='vehicle' AND s.ref_id=v.id AND NOT i.canceled
               AND s.period_key=to_char(p.start_day,CASE WHEN v.billing_period='yearly' THEN 'YYYY' ELSE 'YYYY-MM' END))))`
