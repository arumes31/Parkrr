//go:build integration

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/auth"
	"github.com/preining/parkrr/internal/backup"
	"github.com/preining/parkrr/internal/handlers"
	"github.com/preining/parkrr/internal/mail"
)

func TestLogoutRouteIntegration(t *testing.T) {
	pool := testPool(t)
	manager, err := auth.NewManager(pool, auth.SessionConfig{
		MaxAge: 3600, Sliding: true, AbsoluteMaxAge: 86400,
	}, false, false, "logout-integration-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	manager.SetRequire2FA(true)
	wa, err := auth.NewWebAuthnService(pool, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	router, h, err := New(pool, manager, wa, 0, "", false, false, false,
		"", "", "", backup.S3Config{}, mail.New(mail.Config{}), "", stop, func(func()) {})
	if err != nil {
		t.Fatal(err)
	}
	request := func(cookies []*http.Cookie) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		for _, c := range cookies {
			r.AddCookie(c)
			if c.Name == auth.CSRFCookie {
				r.Header.Set(auth.CSRFHeader, c.Value)
			}
		}
		return r
	}
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	newSession := func(t *testing.T) (int64, []*http.Cookie) {
		t.Helper()
		var id int64
		if err := pool.QueryRow(t.Context(),
			`INSERT INTO users (username,email,password_hash,role,is_admin)
			 VALUES ($1,'','unused','reader',false) RETURNING id`,
			fmt.Sprintf("logout-%d", time.Now().UnixNano())).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
				t.Error(err)
			}
		})
		rec := httptest.NewRecorder()
		if err := manager.CreateSession(t.Context(), rec, request(nil), id); err != nil {
			t.Fatal(err)
		}
		return id, rec.Result().Cookies()
	}
	exists := func(t *testing.T, cookies []*http.Cookie) bool {
		t.Helper()
		var found bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE token=$1)`,
			auth.CurrentToken(request(cookies))).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	confirmed := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ok\"}\n" {
			t.Fatalf("logout not confirmed: status=%d body=%s", rec.Code, rec.Body.String())
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 2 {
			t.Fatalf("want only two clearing cookies (no sliding), got %d", len(cookies))
		}
		for _, c := range cookies {
			if c.Value != "" || c.MaxAge >= 0 || (c.Name != auth.SessionCookie && c.Name != auth.CSRFCookie) {
				t.Fatal("logout refreshed a cookie instead of clearing it")
			}
		}
	}
	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{name: "no cookie"},
		{name: "empty cookie", cookies: []*http.Cookie{{Name: auth.SessionCookie}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // No credential means no DB operation is needed even here.
			confirmed(t, serve(request(tc.cookies).WithContext(ctx)))
		})
	}
	for _, tc := range []struct {
		name  string
		state string
		csrf  string
		want  int
	}{
		{name: "valid session", want: http.StatusOK},
		{name: "expired session", state: "expired", want: http.StatusOK},
		{name: "disabled account", state: "disabled", want: http.StatusOK},
		{name: "already deleted session", state: "deleted", want: http.StatusOK},
		{name: "missing csrf", csrf: "missing", want: http.StatusForbidden},
		{name: "invalid csrf", csrf: "wrong", want: http.StatusForbidden},
		{name: "expired with invalid csrf", state: "expired", csrf: "wrong", want: http.StatusForbidden},
		{name: "disabled with invalid csrf", state: "disabled", csrf: "wrong", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, cookies := newSession(t)
			token := auth.CurrentToken(request(cookies))
			// A valid session is deliberately in the sliding window; logout must
			// never renew it or emit a positive-lifetime cookie before revocation.
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET expires_at=now()+interval '1 minute' WHERE token=$1`, token); err != nil {
				t.Fatal(err)
			}
			var setupErr error
			switch tc.state {
			case "expired":
				_, setupErr = pool.Exec(t.Context(), `UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE token=$1`, token)
			case "disabled":
				_, setupErr = pool.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id=$1`, id)
			case "deleted":
				_, setupErr = pool.Exec(t.Context(), `DELETE FROM sessions WHERE token=$1`, token)
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			other := httptest.NewRecorder()
			if err := manager.CreateSession(t.Context(), other, request(nil), id); err != nil {
				t.Fatal(err)
			}
			req := request(cookies)
			if tc.csrf == "missing" {
				req.Header.Del(auth.CSRFHeader)
			} else if tc.csrf != "" {
				req.Header.Set(auth.CSRFHeader, tc.csrf)
			}
			rec := serve(req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusOK {
				confirmed(t, rec)
				confirmed(t, serve(request(cookies))) // Repeat with the old cookies.
				if tc.state == "disabled" {
					if _, err := pool.Exec(t.Context(), `UPDATE users SET disabled=false WHERE id=$1`, id); err != nil {
						t.Fatal(err)
					}
				}
				me := request(cookies)
				me.Method, me.URL.Path = http.MethodGet, "/api/auth/me"
				if serve(me).Code != http.StatusUnauthorized {
					t.Fatal("logged-out session became usable again, including after account reactivation")
				}
			} else if len(rec.Result().Cookies()) != 0 {
				t.Fatal("CSRF rejection must preserve retry cookies")
			}
			if exists(t, cookies) != (tc.want == http.StatusForbidden) {
				t.Fatal("session presence does not match confirmed/rejected logout")
			}
			if !exists(t, other.Result().Cookies()) {
				t.Fatal("logout revoked another session")
			}
			var audits int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE user_id=$1 AND action='logout'`, id).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			wantAudits := 0
			if tc.want == http.StatusOK && tc.state != "deleted" {
				wantAudits = 1
			}
			if audits != wantAudits {
				t.Fatalf("logout audit count=%d want=%d", audits, wantAudits)
			}
		})
	}
	t.Run("lookup failure retains session and retry cookies", func(t *testing.T) {
		_, cookies := newSession(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		rec := serve(request(cookies).WithContext(ctx))
		if rec.Code != http.StatusInternalServerError || len(rec.Result().Cookies()) != 0 || !exists(t, cookies) {
			t.Fatal("failed lookup was treated as confirmed logout")
		}
		confirmed(t, serve(request(cookies)))
	})
	t.Run("delete failure after successful lookup retains session", func(t *testing.T) {
		id, cookies := newSession(t)
		ah := &handlers.AuthHandler{Handler: h, Auth: manager}
		logCtx := auth.WithRequestLog(t.Context())
		// Inject cancellation at the boundary after the real middleware lookup,
		// before the real handler's DELETE. No schema mutation or fake DB result.
		failing := manager.RequireLogout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := auth.UserFrom(r.Context()); !ok || u.ID != id {
				t.Fatal("failure injection must follow a successful actor lookup")
			}
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			ah.Logout(w, r.WithContext(ctx))
		}))
		rec := httptest.NewRecorder()
		failing.ServeHTTP(rec, request(cookies).WithContext(logCtx))
		if rec.Code != http.StatusInternalServerError || len(rec.Result().Cookies()) != 0 || !exists(t, cookies) {
			t.Fatal("failed DELETE was treated as confirmed logout")
		}
		if auth.RequestError(logCtx) == nil {
			t.Fatal("DELETE failure was not attached to the request log")
		}
		confirmed(t, serve(request(cookies)))
	})
}
