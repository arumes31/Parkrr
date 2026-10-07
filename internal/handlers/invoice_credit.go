package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// creditPaymentIDsTx locks receipts in the same order as reversals. Dedicated
// periodic receipts are never credit, even though they have no allocation row.
func creditPaymentIDsTx(ctx context.Context, tx pgx.Tx, personID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT p.id FROM payments p
		WHERE p.person_id=$1 AND NOT p.reversed AND p.settles_kind IS NULL
		AND p.amount-COALESCE((SELECT sum(amount) FROM payment_allocations WHERE payment_id=p.id),0)
		-COALESCE((SELECT sum(amount) FROM invoice_payments WHERE payment_id=p.id),0)>0.005
		ORDER BY p.id FOR UPDATE`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// applyInvoiceCreditTx allocates existing money, never inserts a new receipt.
// A partial invoice can use several payments; allocation IDs remain auditable.
func (h *Handler) applyInvoiceCreditTx(r *http.Request, tx pgx.Tx, personID int64, paymentIDs []int64) (int, error) {
	if len(paymentIDs) == 0 {
		return 0, nil
	}
	ctx := r.Context()
	rows, err := tx.Query(ctx, `SELECT id, total-paid_amount FROM invoices
		WHERE person_id=$1 AND NOT canceled AND cancels_id IS NULL AND total-paid_amount>0.005
		ORDER BY id FOR UPDATE`, personID)
	if err != nil {
		return 0, err
	}
	type openInvoice struct {
		id     int64
		amount float64
	}
	open := []openInvoice{}
	for rows.Next() {
		var iv openInvoice
		if err := rows.Scan(&iv.id, &iv.amount); err != nil {
			rows.Close()
			return 0, err
		}
		open = append(open, iv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	count := 0
	for _, iv := range open {
		applied := 0.0
		for _, paymentID := range paymentIDs {
			if iv.amount < 0.005 {
				break
			}
			var available float64
			if err := tx.QueryRow(ctx, `SELECT p.amount
				-COALESCE((SELECT sum(amount) FROM payment_allocations WHERE payment_id=p.id),0)
				-COALESCE((SELECT sum(amount) FROM invoice_payments WHERE payment_id=p.id),0)
				FROM payments p WHERE id=$1 AND NOT reversed`, paymentID).Scan(&available); err != nil {
				return 0, err
			}
			amount := round2(min(available, iv.amount))
			if amount < 0.005 {
				continue
			}
			if err := protectPaymentChangesTx(ctx, tx, []int64{paymentID}); err != nil {
				return 0, err
			}
			var allocationID int64
			if err := tx.QueryRow(ctx, `INSERT INTO invoice_payments(invoice_id,payment_id,amount)
				VALUES($1,$2,$3) RETURNING id`, iv.id, paymentID, amount).Scan(&allocationID); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(ctx, `UPDATE invoices SET paid_amount=paid_amount+$2 WHERE id=$1`, iv.id, amount); err != nil {
				return 0, err
			}
			if err := h.auditCreatedTx(ctx, tx, r, "invoice_payment", allocationID,
				fmt.Sprintf("Guthaben %.2f € zugeordnet, kein neuer Zahlungseingang", amount),
				map[string]any{"invoice_id": iv.id, "payment_id": paymentID, "amount": amount}); err != nil {
				return 0, err
			}
			iv.amount = round2(iv.amount - amount)
			applied += amount
		}
		if applied > 0.005 {
			count++
		}
	}
	return count, nil
}
