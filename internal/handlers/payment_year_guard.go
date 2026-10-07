package handlers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func acquireTaxYearWriteLock(ctx context.Context, tx pgx.Tx, year int) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, advisoryKey("parkrr.tax-year."+strconv.Itoa(year)))
	return err
}

// protectPaymentChangesTx shares the closing lock with LockTaxYear. Payment
// rows come first, then years in ascending order, like the metadata editor.
// Both receipt and optional rule years matter: neither closed view may change.
func protectPaymentChangesTx(ctx context.Context, tx pgx.Tx, ids []int64, dates ...time.Time) error {
	years := map[int]bool{}
	for _, date := range dates {
		years[date.Year()] = true
	}
	if len(ids) > 0 {
		// Lock first, then read metadata with a fresh READ COMMITTED snapshot.
		// A joined SELECT that waited for p could otherwise retain old metadata.
		locks, err := tx.Query(ctx, `SELECT id FROM payments WHERE id=ANY($1) ORDER BY id FOR UPDATE`, ids)
		if err != nil {
			return fmt.Errorf("lock payments for tax protection: %w", err)
		}
		for locks.Next() {
			var id int64
			if err := locks.Scan(&id); err != nil {
				locks.Close()
				return err
			}
		}
		locks.Close()
		if err := locks.Err(); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT p.paid_on, COALESCE(m.received_on,p.paid_on),
			COALESCE(p.settles_kind,''), COALESCE(p.settles_period,'')
			FROM payments p LEFT JOIN payment_tax_metadata m ON m.payment_id=p.id
			WHERE p.id=ANY($1) ORDER BY p.id`, ids)
		if err != nil {
			return fmt.Errorf("lock payment receipt dates: %w", err)
		}
		for rows.Next() {
			var booked, received time.Time
			var kind, period string
			if err := rows.Scan(&booked, &received, &kind, &period); err != nil {
				rows.Close()
				return fmt.Errorf("read payment receipt dates: %w", err)
			}
			years[booked.Year()], years[received.Year()] = true, true
			years[taxRuleYear(received, kind, period)] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read payment receipt dates: %w", err)
		}
	}
	ordered := make([]int, 0, len(years))
	for year := range years {
		ordered = append(ordered, year)
	}
	sort.Ints(ordered)
	for _, year := range ordered {
		locked, err := taxYearLocked(ctx, tx, year)
		if err != nil {
			return fmt.Errorf("check payment tax year: %w", err)
		}
		if locked {
			return &settlementConflictError{fmt.Sprintf("Steuerjahr %d ist gesperrt – vor einer Korrektur ausdrücklich wieder öffnen", year)}
		}
	}
	return nil
}

// protectPeriodPaymentsTx protects every existing receipt before a bulk reset.
func protectPeriodPaymentsTx(ctx context.Context, tx pgx.Tx, kind string, refID int64, periods ...string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM payments WHERE auto AND settles_kind=$1 AND settles_ref=$2
		AND ($3::text[] IS NULL OR settles_period=ANY($3)) ORDER BY id`, kind, refID, periods)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return protectPaymentChangesTx(ctx, tx, ids)
}

func protectCurrentReceiptTx(ctx context.Context, tx pgx.Tx, kind, period string) error {
	var today time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_DATE`).Scan(&today); err != nil {
		return err
	}
	ruleDate := time.Date(taxRuleYear(today, kind, period), 1, 1, 0, 0, 0, 0, time.UTC)
	return protectPaymentChangesTx(ctx, tx, nil, today, ruleDate)
}
