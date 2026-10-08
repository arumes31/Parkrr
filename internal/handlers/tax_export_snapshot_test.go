package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestBuildTaxExportSnapshotFailureCleanup(t *testing.T) {
	queryErr := errors.New("read unavailable")
	rollbackErr := errors.New("rollback unavailable")
	for _, tc := range []struct {
		name        string
		beginErr    error
		queryErr    error
		rollbackErr error
		isCanceled  bool
	}{
		{name: "begin", beginErr: queryErr},
		{name: "query", queryErr: queryErr},
		{name: "query and rollback", queryErr: queryErr, rollbackErr: rollbackErr},
		{name: "canceled request", queryErr: context.Canceled, isCanceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := taxExportTestTempDir(t)
			tx := &taxSnapshotFailureTx{queryErr: tc.queryErr, rollbackErr: tc.rollbackErr}
			db := &taxSnapshotFailureDB{tx: tx, err: tc.beginErr}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.isCanceled {
				cancel()
			}
			snapshot, err := buildTaxExportSnapshot(ctx, db, 2090)
			if snapshot != nil || err == nil {
				t.Fatalf("snapshot escaped failed read: %v, %v", snapshot, err)
			}
			wantErr := tc.queryErr
			if tc.beginErr != nil {
				wantErr = tc.beginErr
			}
			if !errors.Is(err, wantErr) || (tc.rollbackErr != nil && !errors.Is(err, tc.rollbackErr)) {
				t.Fatalf("lost error chain: %v", err)
			}
			if db.options.IsoLevel != pgx.RepeatableRead || db.options.AccessMode != pgx.ReadOnly {
				t.Fatalf("unsafe transaction options: %+v", db.options)
			}
			if tc.beginErr == nil && (!tx.isRolledBack || tx.rollbackContextErr != nil) {
				t.Fatalf("cleanup missing or used canceled context: %+v", tx)
			}
			assertTaxExportTempEmpty(t, dir)
		})
	}
}

func TestBuildTaxExportSnapshotTempUnavailable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	db := &taxSnapshotFailureDB{}
	if snapshot, err := buildTaxExportSnapshot(t.Context(), db, 2090); err == nil || snapshot != nil {
		t.Fatalf("unavailable spool accepted: %v, %v", snapshot, err)
	}
	if db.isBegun {
		t.Fatal("database connection borrowed before temporary storage was available")
	}
}

func TestSpoolTaxExportReceiptsLimitsAndFailures(t *testing.T) {
	writeErr := errors.New("no space left on device")
	iterationErr := errors.New("receipt read interrupted")
	for _, tc := range []struct {
		name    string
		total   int64
		writer  io.Writer
		rowErr  error
		wantErr error
	}{
		{name: "preflight limit", total: defaultTaxExportReceiptBytes + 1, writer: io.Discard, wantErr: errTaxExportReceiptLimit},
		{name: "concurrent export fills disk", total: 3, writer: taxSnapshotFailureWriter{err: writeErr}, wantErr: writeErr},
		{name: "short write", total: 3, writer: taxSnapshotFailureWriter{}, wantErr: io.ErrShortWrite},
		{name: "iteration", total: 3, writer: io.Discard, rowErr: iterationErr, wantErr: iterationErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PARKRR_TAX_EXPORT_MAX_RECEIPT_MB", "")
			rows := &taxSnapshotReceiptRows{data: []byte("PDF"), err: tc.rowErr}
			q := &taxSnapshotReceiptDB{total: tc.total, rows: rows}
			if _, err := spoolTaxExportReceipts(t.Context(), q, 2090, tc.writer); !errors.Is(err, tc.wantErr) {
				t.Fatalf("lost failure: %v, want %v", err, tc.wantErr)
			}
			if tc.total > defaultTaxExportReceiptBytes {
				if q.isQueried {
					t.Fatal("oversized receipt blobs fetched despite preflight")
				}
			} else if !rows.isClosed {
				t.Fatal("receipt rows left open after error")
			}
		})
	}
}

func TestTaxExportReceiptByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  int64
	}{
		{name: "default", want: defaultTaxExportReceiptBytes},
		{name: "small volume", value: "1", want: 1 << 20},
		{name: "dedicated volume", value: "1024", want: 1024 << 20},
		{name: "zero", value: "0"},
		{name: "negative", value: "-1"},
		{name: "over maximum", value: "1025"},
		{name: "invalid", value: "unbounded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PARKRR_TAX_EXPORT_MAX_RECEIPT_MB", tc.value)
			got, err := taxExportReceiptByteLimit()
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid resource limit accepted")
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("limit %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestTaxExportContextReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := taxExportContextReader{ctx: ctx, src: bytes.NewReader([]byte("private receipt"))}
	var dst bytes.Buffer
	if n, err := io.Copy(&dst, reader); n != 0 || !errors.Is(err, context.Canceled) || dst.Len() != 0 {
		t.Fatalf("canceled download continued: %d, %v", n, err)
	}
}

func taxExportTestTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	return dir
}

func assertTaxExportTempEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private receipt spool left behind: %v", entries)
	}
}

type taxSnapshotFailureDB struct {
	tx      pgx.Tx
	err     error
	options pgx.TxOptions
	isBegun bool
}

func (d *taxSnapshotFailureDB) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	d.isBegun, d.options = true, options
	return d.tx, d.err
}

type taxSnapshotFailureTx struct {
	pgx.Tx
	queryErr           error
	rollbackErr        error
	rollbackContextErr error
	isRolledBack       bool
}

func (t *taxSnapshotFailureTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return taxSnapshotFailureRow{err: t.queryErr}
}

func (t *taxSnapshotFailureTx) Rollback(ctx context.Context) error {
	t.isRolledBack, t.rollbackContextErr = true, ctx.Err()
	return t.rollbackErr
}

type taxSnapshotFailureRow struct{ err error }

func (r taxSnapshotFailureRow) Scan(...any) error { return r.err }

type taxSnapshotFailureWriter struct{ err error }

func (w taxSnapshotFailureWriter) Write([]byte) (int, error) { return 0, w.err }

type taxSnapshotReceiptDB struct {
	dbQuerier
	total     int64
	rows      pgx.Rows
	isQueried bool
}

func (q *taxSnapshotReceiptDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return taxSnapshotSizeRow{size: q.total}
}

func (q *taxSnapshotReceiptDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	q.isQueried = true
	return q.rows, nil
}

type taxSnapshotSizeRow struct{ size int64 }

func (r taxSnapshotSizeRow) Scan(values ...any) error {
	*values[0].(*int64) = r.size
	return nil
}

type taxSnapshotReceiptRows struct {
	pgx.Rows
	data     []byte
	err      error
	isRead   bool
	isClosed bool
}

func (r *taxSnapshotReceiptRows) Next() bool {
	if r.isRead {
		return false
	}
	r.isRead = true
	return true
}

func (r *taxSnapshotReceiptRows) Scan(values ...any) error {
	*values[0].(*int64) = 1
	*values[1].(*int64) = 2
	*values[2].(*string) = "../receipt.pdf"
	*values[3].(*[]byte) = r.data
	return nil
}

func (r *taxSnapshotReceiptRows) Close()     { r.isClosed = true }
func (r *taxSnapshotReceiptRows) Err() error { return r.err }
