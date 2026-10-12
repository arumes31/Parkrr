package handlers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/preining/parkrr/internal/config"
)

// The hardened deployment reserves 32 MiB for /tmp. Fail before the response
// starts rather than filling that volume or retaining every receipt in RAM.
const defaultTaxExportReceiptBytes = config.DefaultTaxExportMaxReceiptBytes

var errTaxExportReceiptLimit = errors.New("tax export receipts exceed temporary storage limit")

type taxExportBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type taxExportReceipt struct {
	ReceiptID int64  `json:"receipt_id"`
	ExpenseID int64  `json:"expense_id"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	offset    int64
	size      int64
}

type taxExportSnapshot struct {
	Report    taxYearReport
	History   taxPaymentHistory
	Receipts  []taxExportReceipt
	spool     *os.File
	byteLimit int64
}

// buildTaxExportSnapshot materializes every database-backed export component
// before releasing its one read-only snapshot. Only raw receipt bytes go to a
// private temporary file; PDF/ZIP compression and slow clients hold no DB rows.
func buildTaxExportSnapshot(
	ctx context.Context,
	db taxExportBeginner,
	year int,
	byteLimit int64,
) (out *taxExportSnapshot, err error) {
	if byteLimit == 0 {
		byteLimit = defaultTaxExportReceiptBytes
	}
	if byteLimit < 0 {
		return nil, errors.New("tax export receipt limit must be positive")
	}
	spool, err := os.CreateTemp("", "parkrr-tax-export-*")
	if err != nil {
		return nil, fmt.Errorf("create tax receipt spool: %w", err)
	}
	snapshot := &taxExportSnapshot{spool: spool, byteLimit: byteLimit}
	retained := false
	defer func() {
		if !retained {
			err = errors.Join(err, snapshot.close())
		}
	}()
	if err := readTaxExportSnapshotTx(ctx, db, year, snapshot); err != nil {
		return nil, err
	}
	retained = true
	return snapshot, nil
}

func readTaxExportSnapshotTx(ctx context.Context, db taxExportBeginner, year int, snapshot *taxExportSnapshot) (err error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin tax export snapshot: %w", err)
	}
	defer func() {
		// Request cancellation must not prevent releasing a borrowed connection.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanupCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback tax export snapshot: %w", rollbackErr))
		}
	}()
	if snapshot.Report, err = buildTaxYearReport(ctx, tx, year); err != nil {
		return fmt.Errorf("read tax export report: %w", err)
	}
	if snapshot.History, err = loadTaxPaymentHistory(ctx, tx, snapshot.Report); err != nil {
		return fmt.Errorf("read tax export history: %w", err)
	}
	if snapshot.Receipts, err = spoolTaxExportReceipts(
		ctx,
		tx,
		year,
		snapshot.spool,
		snapshot.byteLimit,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tax export snapshot: %w", err)
	}
	return nil
}

func spoolTaxExportReceipts(
	ctx context.Context,
	q dbQuerier,
	year int,
	dst io.Writer,
	limit int64,
) ([]taxExportReceipt, error) {
	// Check total size before reading bytea values, within the same snapshot.
	var total int64
	if err := q.QueryRow(ctx, `SELECT COALESCE(sum(octet_length(a.data)),0)
		FROM tax_expense_receipts a JOIN tax_expenses e ON e.id=a.expense_id
		WHERE e.paid_on >= make_date($1,1,1) AND e.paid_on < make_date($1+1,1,1)`, year).Scan(&total); err != nil {
		return nil, fmt.Errorf("measure tax export receipts: %w", err)
	}
	if total > limit {
		return nil, errTaxExportReceiptLimit
	}
	rows, err := q.Query(ctx, `SELECT a.id,a.expense_id,a.filename,a.data
		FROM tax_expense_receipts a JOIN tax_expenses e ON e.id=a.expense_id
		WHERE e.paid_on >= make_date($1,1,1) AND e.paid_on < make_date($1+1,1,1)
		ORDER BY e.paid_on,e.id,a.id`, year)
	if err != nil {
		return nil, fmt.Errorf("query tax export receipts: %w", err)
	}
	defer rows.Close()
	entries := []taxExportReceipt{}
	var offset int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var entry taxExportReceipt
		var filename string
		var data []byte
		if err := rows.Scan(&entry.ReceiptID, &entry.ExpenseID, &filename, &data); err != nil {
			return nil, fmt.Errorf("scan tax export receipt: %w", err)
		}
		entry.size = int64(len(data))
		if entry.size > limit-offset {
			return nil, errTaxExportReceiptLimit
		}
		base := safeDownloadFilename(filepath.Base(filename))
		if base == "" || base == "." {
			base = "beleg"
		}
		entry.Path = fmt.Sprintf("belege/ausgabe-%d-%d-%s", entry.ExpenseID, entry.ReceiptID, strings.ReplaceAll(base, "/", "_"))
		entry.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		entry.offset = offset
		n, err := dst.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return nil, fmt.Errorf("spool tax export receipt: %w", err)
		}
		offset += entry.size
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tax export receipts: %w", err)
	}
	return entries, nil
}

func (s *taxExportSnapshot) close() error {
	if s.spool == nil {
		return nil
	}
	name := s.spool.Name()
	closeErr := s.spool.Close()
	s.spool = nil
	// The exact path is returned by CreateTemp, never derived from request data.
	removeErr := os.Remove(name) // #nosec G703 -- private server-created temporary file
	if err := errors.Join(closeErr, removeErr); err != nil {
		return fmt.Errorf("remove tax export receipt spool: %w", err)
	}
	return nil
}

type taxExportContextReader struct {
	ctx context.Context
	src io.Reader
}

func (r taxExportContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
