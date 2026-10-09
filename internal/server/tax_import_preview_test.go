//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/auth"
	"github.com/preining/parkrr/internal/backup"
	"github.com/preining/parkrr/internal/mail"
)

func TestTaxImportPreviewRouteRequiresAdminAndCSRFInReadOnlyMode(t *testing.T) {
	pool := testPool(t)
	manager, err := auth.NewManager(
		pool,
		auth.SessionConfig{MaxAge: 3600},
		false,
		false,
		"tax-import-test-session-secret-long-enough",
	)
	if err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthnService(
		pool,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// Disable unrelated startup bookkeeping; no preview should start ledger writes.
	handler, h, err := New(
		pool,
		manager,
		wa,
		0,
		"",
		false,
		false,
		false,
		"",
		"",
		"",
		backup.S3Config{},
		mail.New(mail.Config{}),
		"",
		stop,
		func(func()) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	if h.TaxWritesEnabled {
		t.Fatal("fixture must retain startup default read-only mode")
	}
	cookiesFor := func(role string) []*http.Cookie {
		t.Helper()
		var id int64
		err := pool.QueryRow(
			t.Context(),
			`INSERT INTO users (username,email,password_hash,role,is_admin)
			 VALUES ($1,'','unused',$2,$3) RETURNING id`,
			fmt.Sprintf("tax-import-%s-%d", role, time.Now().UnixNano()),
			role,
			role == "admin",
		).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
		rec := httptest.NewRecorder()
		if err := manager.CreateSession(
			t.Context(),
			rec,
			httptest.NewRequest(http.MethodGet, "/", nil),
			id,
		); err != nil {
			t.Fatal(err)
		}
		return rec.Result().Cookies()
	}
	admin, editor, reader := cookiesFor("admin"), cookiesFor("editor"), cookiesFor("reader")
	var beforeAudit int64
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log`).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	var firstPreview string
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		csrf    bool
		want    int
	}{
		{name: "anonymous", cookies: []*http.Cookie{}, want: http.StatusUnauthorized},
		{name: "admin without csrf", cookies: admin, want: http.StatusForbidden},
		{name: "editor with csrf", cookies: editor, csrf: true, want: http.StatusForbidden},
		{name: "reader with csrf", cookies: reader, csrf: true, want: http.StatusForbidden},
		{name: "admin read-only preview", cookies: admin, csrf: true, want: http.StatusOK},
		{name: "admin read-only replay", cookies: admin, csrf: true, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The external object ID is intentionally not a seeded property: the preview
			// cannot claim that caller-provided scope has been checked against the DB.
			body := `{"version":1,"year":2025,"property_id":9007199254740991,` +
				`"source":"synthetic","reviewer":"test","entries":[` +
				`{"external_id":"test-1","kind":"external_depreciation",` +
				`"amount_eur":"123.45","description":"External annual amount"}]}`
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/reports/tax-import/preview",
				strings.NewReader(body),
			)
			req.Header.Set("Content-Type", "application/json")
			for _, cookie := range tc.cookies {
				req.AddCookie(cookie)
				if tc.csrf && cookie.Name == auth.CSRFCookie {
					req.Header.Set(auth.CSRFHeader, cookie.Value)
				}
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			var rep struct{ Preview, Persisted, Verified bool }
			if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
				t.Fatal(err)
			}
			if !rep.Preview || rep.Persisted || rep.Verified {
				t.Fatalf("not an unverified stateless preview: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("preview must not be cached")
			}
			if firstPreview == "" {
				firstPreview = rec.Body.String()
			}
			if rec.Body.String() != firstPreview {
				t.Fatal("authenticated replay changed the preview")
			}
		})
	}
	var afterAudit int64
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log`).Scan(&afterAudit); err != nil {
		t.Fatal(err)
	}
	if afterAudit != beforeAudit {
		t.Fatalf("preview/auth checks wrote an audit row: before=%d after=%d", beforeAudit, afterAudit)
	}
}
