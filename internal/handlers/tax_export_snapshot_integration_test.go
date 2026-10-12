//go:build integration

package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTaxExportSnapshotConcurrentCommit(t *testing.T) {
	dir := taxExportTestTempDir(t)
	h := testHandler(t)
	f := createTaxExportSnapshotFixture(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			done <- ctx.Err()
			return
		}
		done <- pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			statements := []struct {
				sql  string
				args []any
			}{
				{`UPDATE payments SET reversed=true,reversed_at=now() WHERE id=$1`, []any{f.paymentID}},
				{`DELETE FROM payment_allocations WHERE payment_id=$1`, []any{f.paymentID}},
				{`UPDATE tax_properties SET name='Snapshot after' WHERE id=$1`, []any{f.propertyID}},
				{`INSERT INTO tax_expenses(tax_property_id,category_id,paid_on,amount,description)
					SELECT tax_property_id,category_id,paid_on,5,'concurrent expense' FROM tax_expenses WHERE id=$1`, []any{f.expenseID}},
				{`INSERT INTO tax_assets(tax_property_id,name,in_service_on,depreciable_basis,useful_life_years,half_year_rule)
					VALUES($1,'concurrent asset','2090-01-01',50,5,false)`, []any{f.propertyID}},
				{`INSERT INTO tax_expense_receipts(expense_id,filename,content_type,byte_size,sha256,data)
					VALUES($1,'new.pdf','application/pdf',3,'test',$2)`, []any{f.expenseID, []byte("NEW")}},
				{`INSERT INTO tax_year_locks(year) VALUES(2090)`, nil},
				{`INSERT INTO audit_log(action,entity,entity_id,summary,changes)
					VALUES('update','payment',$1,'concurrent reversal','{"reversed":{"old":false,"new":true},"released_allocations":[]}')`, []any{f.paymentID}},
			}
			for _, statement := range statements {
				if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
					return err
				}
			}
			return nil
		})
	}()
	db := &taxSnapshotHookDB{pool: h.Pool, afterFirstRead: func() error {
		close(ready)
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	snapshot, err := buildTaxExportSnapshot(
		ctx,
		db,
		2090,
		defaultTaxExportReceiptBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if snapshot.Report.TotalByDate != 100 || snapshot.Report.ExpenseTotal != 20 || snapshot.Report.DepreciationTotal != 10 || snapshot.Report.Locked {
		t.Fatalf("summary crossed snapshot boundary: %+v", snapshot.Report)
	}
	if snapshot.History.Reversed != 0 || bytes.Contains(snapshot.History.Audit.Bytes(), []byte("concurrent reversal")) {
		t.Fatal("history/audit observed later reversal")
	}
	links := historyCSVRows(t, snapshot.History.Links.Bytes())
	if len(links) != 1 || links[0]["payment_id"] != strconv.FormatInt(f.paymentID, 10) {
		t.Fatalf("allocation not in original snapshot: %v", links)
	}
	if len(snapshot.Receipts) != 1 || snapshot.Report.Expenses[0].ReceiptCount != 1 {
		t.Fatal("receipt count and blobs crossed snapshot boundary")
	}
	receipt := snapshot.Receipts[0]
	data, err := io.ReadAll(io.NewSectionReader(snapshot.spool, receipt.offset, receipt.size))
	if err != nil || string(data) != "OLD" || receipt.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("original receipt missing or corrupt: %q, %v", data, err)
	}
	if !strings.Contains(snapshot.Report.payments[0].Property, "Snapshot before") {
		t.Fatalf("current label leaked into snapshot: %q", snapshot.Report.payments[0].Property)
	}
	if !db.tx.isCommitted || !db.tx.isRolledBack {
		t.Fatal("transaction was not closed before snapshot returned")
	}
	if err := snapshot.close(); err != nil {
		t.Fatal(err)
	}
	assertTaxExportTempEmpty(t, dir)
	next, err := buildTaxExportSnapshot(
		ctx,
		h.Pool,
		2090,
		defaultTaxExportReceiptBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := next.close(); err != nil {
			t.Error(err)
		}
	}()
	if next.Report.TotalByDate != 0 || next.Report.ExpenseTotal != 25 || next.Report.DepreciationTotal != 20 || !next.Report.Locked || len(next.Receipts) != 2 || next.History.Reversed != 1 {
		t.Fatalf("next export missed committed changes: %+v", next.Report)
	}
}

func TestTaxExportSnapshotCommitFailureCleanup(t *testing.T) {
	dir := taxExportTestTempDir(t)
	h := testHandler(t)
	createTaxExportSnapshotFixture(t, h)
	commitErr := errors.New("commit unavailable")
	db := &taxSnapshotHookDB{pool: h.Pool, commitErr: commitErr}
	snapshot, err := buildTaxExportSnapshot(
		t.Context(),
		db,
		2090,
		defaultTaxExportReceiptBytes,
	)
	if snapshot != nil || !errors.Is(err, commitErr) || !db.tx.isRolledBack {
		t.Fatalf("failed commit exposed snapshot or leaked transaction: %v, %v", snapshot, err)
	}
	assertTaxExportTempEmpty(t, dir)
	if h.Pool.Stat().AcquiredConns() != 0 {
		t.Fatal("failed export retained a pool connection")
	}
}

func TestTaxExportSnapshotReleasesConnectionBeforeResponse(t *testing.T) {
	dir := taxExportTestTempDir(t)
	h := testHandler(t)
	createTaxExportSnapshotFixture(t, h)
	cfg := h.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	single := New(pool)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/reports/tax-year.zip?year=2090", nil).WithContext(ctx)
	writer := &taxSnapshotResponseWriter{ResponseRecorder: httptest.NewRecorder(), pool: pool}
	single.TaxYearPackage(writer, request)
	if writer.err != nil {
		t.Fatal(writer.err)
	}
	if writer.Code != http.StatusOK || !writer.isChecked || writer.Body.Len() == 0 {
		t.Fatalf("export failed: %d, %s", writer.Code, writer.Body.String())
	}
	assertTaxExportTempEmpty(t, dir)
	files := readTaxHistoryPackage(t, single, 2090)
	var manifest struct {
		SingleSnapshot bool `json:"package_single_snapshot"`
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil || !manifest.SingleSnapshot {
		t.Fatalf("manifest does not prove snapshot scope: %+v, %v", manifest, err)
	}
	assertTaxExportTempEmpty(t, dir)
	// A disconnect after headers must still remove the private receipt file.
	failed := &taxSnapshotResponseWriter{ResponseRecorder: httptest.NewRecorder(), pool: pool, writeErr: io.ErrClosedPipe}
	single.TaxYearPackage(failed, request)
	assertTaxExportTempEmpty(t, dir)
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("download failure retained connection")
	}
}

func TestTaxExportSnapshotSizeLimitBeforeHeaders(t *testing.T) {
	dir := taxExportTestTempDir(t)
	h := testHandler(t)
	h.TaxExportMaxReceiptBytes = 1 << 20
	f := createTaxExportSnapshotFixture(t, h)
	// The original 3-byte receipt makes the combined size exceed the 1 MiB
	// configured allowance. No blobs should be fetched or response ZIP started.
	data := bytes.Repeat([]byte("x"), 1<<20)
	if _, err := h.Pool.Exec(t.Context(), `INSERT INTO tax_expense_receipts(expense_id,filename,content_type,byte_size,sha256,data)
		VALUES($1,'limit.pdf','application/pdf',$2,'test',$3)`, f.expenseID, len(data), data); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.TaxYearPackage(rec, httptest.NewRequest(http.MethodGet, "/api/reports/tax-year.zip?year=2090", nil))
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") == "application/zip" || rec.Header().Get("Content-Disposition") != "" {
		t.Fatalf("oversized package started a download: %d, %v", rec.Code, rec.Header())
	}
	assertTaxExportTempEmpty(t, dir)
	var count int
	if err := h.Pool.QueryRow(t.Context(), `SELECT count(*) FROM tax_expense_receipts WHERE expense_id=$1`, f.expenseID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejected export changed receipt records: %d, %v", count, err)
	}
	if h.Pool.Stat().AcquiredConns() != 0 {
		t.Fatal("size rejection retained connection")
	}
}

type taxSnapshotFixture struct{ propertyID, expenseID, paymentID int64 }

func createTaxExportSnapshotFixture(t *testing.T, h *Handler) taxSnapshotFixture {
	t.Helper()
	f := taxSnapshotFixture{}
	ctx := t.Context()
	pid := createIntegrationPerson(t, h)
	if err := h.Pool.QueryRow(ctx, `INSERT INTO tax_properties(name) VALUES('Snapshot before') RETURNING id`).Scan(&f.propertyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM tax_year_locks WHERE year=2090`,
			`DELETE FROM tax_expense_receipts WHERE expense_id IN (SELECT id FROM tax_expenses WHERE tax_property_id=$1)`,
			`DELETE FROM tax_expenses WHERE tax_property_id=$1`,
			`DELETE FROM tax_assets WHERE tax_property_id=$1`,
			`DELETE FROM payment_tax_metadata WHERE tax_property_id=$1`,
			`DELETE FROM tax_properties WHERE id=$1`,
		} {
			var args []any
			if strings.Contains(statement, "$1") {
				args = []any{f.propertyID}
			}
			if err := purgeExec(context.Background(), h.Pool, statement, args...); err != nil {
				t.Error(err)
			}
		}
	})
	if err := h.Pool.QueryRow(ctx, `INSERT INTO payments(person_id,amount,paid_on,method)
		VALUES($1,100,'2090-06-01','bar') RETURNING id`, pid).Scan(&f.paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Pool.Exec(ctx, `INSERT INTO payment_tax_metadata(payment_id,tax_property_id) VALUES($1,$2)`, f.paymentID, f.propertyID); err != nil {
		t.Fatal(err)
	}
	cid := mkChargeP(t, h, pid, "Snapshot rent", 100, "2090-05-01")
	if _, err := h.Pool.Exec(ctx, `INSERT INTO payment_allocations(payment_id,kind,ref_id,amount) VALUES($1,'charge',$2,100)`, f.paymentID, cid); err != nil {
		t.Fatal(err)
	}
	if err := h.Pool.QueryRow(ctx, `INSERT INTO tax_expenses(tax_property_id,category_id,paid_on,amount,description)
		SELECT $1,id,'2090-05-01',20,'snapshot expense' FROM tax_expense_categories WHERE key='maintenance' RETURNING id`, f.propertyID).Scan(&f.expenseID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Pool.Exec(ctx, `INSERT INTO tax_expense_receipts(expense_id,filename,content_type,byte_size,sha256,data)
		VALUES($1,'old.pdf','application/pdf',3,'test',$2)`, f.expenseID, []byte("OLD")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Pool.Exec(ctx, `INSERT INTO tax_assets(tax_property_id,name,in_service_on,depreciable_basis,useful_life_years,half_year_rule)
		VALUES($1,'snapshot asset','2090-01-01',100,10,false)`, f.propertyID); err != nil {
		t.Fatal(err)
	}
	return f
}

type taxSnapshotHookDB struct {
	pool           *pgxpool.Pool
	afterFirstRead func() error
	commitErr      error
	tx             *taxSnapshotHookTx
}

func (d *taxSnapshotHookDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := d.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	d.tx = &taxSnapshotHookTx{Tx: tx, afterFirstRead: d.afterFirstRead, commitErr: d.commitErr}
	return d.tx, nil
}

type taxSnapshotHookTx struct {
	pgx.Tx
	afterFirstRead func() error
	commitErr      error
	isCommitted    bool
	isRolledBack   bool
}

func (t *taxSnapshotHookTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := t.Tx.QueryRow(ctx, sql, args...)
	if t.afterFirstRead == nil {
		return row
	}
	hook := t.afterFirstRead
	t.afterFirstRead = nil
	return taxSnapshotHookRow{Row: row, afterRead: hook}
}

func (t *taxSnapshotHookTx) Commit(ctx context.Context) error {
	if t.commitErr != nil {
		return t.commitErr
	}
	err := t.Tx.Commit(ctx)
	t.isCommitted = err == nil
	return err
}

func (t *taxSnapshotHookTx) Rollback(ctx context.Context) error {
	t.isRolledBack = true
	return t.Tx.Rollback(ctx)
}

type taxSnapshotHookRow struct {
	pgx.Row
	afterRead func() error
}

func (r taxSnapshotHookRow) Scan(values ...any) error {
	if err := r.Row.Scan(values...); err != nil {
		return err
	}
	return r.afterRead()
}

type taxSnapshotResponseWriter struct {
	*httptest.ResponseRecorder
	pool      *pgxpool.Pool
	writeErr  error
	err       error
	isChecked bool
}

func (w *taxSnapshotResponseWriter) Write(data []byte) (int, error) {
	if !w.isChecked {
		w.isChecked = true
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		w.err = w.pool.Ping(ctx)
		if w.pool.Stat().AcquiredConns() != 0 {
			w.err = errors.Join(w.err, errors.New("export retained a database connection while sending response"))
		}
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(data)
}

// Ensure restrictive file creation is checked where POSIX mode bits are meaningful.
func TestTaxExportSnapshotTempPermissions(t *testing.T) {
	dir := taxExportTestTempDir(t)
	h := testHandler(t)
	snapshot, err := buildTaxExportSnapshot(
		t.Context(),
		h.Pool,
		2090,
		defaultTaxExportReceiptBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	info, statErr := snapshot.spool.Stat()
	closeErr := snapshot.close()
	if err := errors.Join(statErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if os.PathSeparator != '\\' && info.Mode().Perm() != 0600 {
		t.Fatalf("receipt file permissions: %o", info.Mode().Perm())
	}
	assertTaxExportTempEmpty(t, dir)
}
