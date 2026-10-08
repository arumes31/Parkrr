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

func TestTaxReviewRouteRequiresAdminAndCSRFInReadOnlyMode(t *testing.T) {
	t.Setenv("PARKRR_TAX_WRITES_ENABLED", "false")
	pool := testPool(t)
	manager, err := auth.NewManager(pool, auth.SessionConfig{MaxAge: 3600}, false, false,
		"tax-preview-test-session-secret-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthnService(pool, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	// Background bookkeeping is unrelated to this route and must not mask its purity.
	handler, _, err := New(pool, manager, wa, 0, "", false, false, false,
		"", "", "", backup.S3Config{}, mail.New(mail.Config{}), "", stop, func(func()) {})
	if err != nil {
		t.Fatal(err)
	}
	cookiesFor := func(role string) []*http.Cookie {
		t.Helper()
		var id int64
		err := pool.QueryRow(t.Context(), `INSERT INTO users (username,email,password_hash,role,is_admin)
			VALUES ($1,'','unused',$2,$3) RETURNING id`,
			fmt.Sprintf("tax-preview-%s-%d", role, time.Now().UnixNano()), role, role == "admin").Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		rec := httptest.NewRecorder()
		if err := manager.CreateSession(t.Context(), rec, httptest.NewRequest(http.MethodGet, "/", nil), id); err != nil {
			t.Fatal(err)
		}
		return rec.Result().Cookies()
	}
	admin, editor := cookiesFor("admin"), cookiesFor("editor")
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
		csrf    bool
		want    int
	}{
		{"anonymous", nil, false, http.StatusUnauthorized},
		{"admin without csrf", admin, false, http.StatusForbidden},
		{"editor with csrf", editor, true, http.StatusForbidden},
		{"admin read-only preview", admin, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/reports/tax-review/preview",
				strings.NewReader(`{"year":2025,"turnover":{"current_eur":"100","previous_eur":"0","opted_out":false,"source":"synthetic","reviewer":"test"}}`))
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
			if tc.want == http.StatusOK {
				var body struct{ Preview, Persisted bool }
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Preview || body.Persisted {
					t.Fatalf("not a stateless preview: %s (%v)", rec.Body.String(), err)
				}
				if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
					t.Fatal("preview must not be cached")
				}
			}
		})
	}
}
