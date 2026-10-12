//go:build integration

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/preining/parkrr/internal/auth"
)

func TestDeletePasskeyIntegration(t *testing.T) {
	const password = "delete-passkey-correct-password"
	cases := []struct {
		name          string
		body          string
		stale         bool
		pairLocked    bool
		accountLocked bool
		priorFailures int
		otherOwner    bool
		missing       bool
		passkeyOnly   bool
		lastKey       bool
		disabled      bool
		noSession     bool
		noCSRF        bool
		expired       bool
		want          int
		wantError     string
		remaining     int
	}{
		{name: "recent empty body", want: http.StatusOK, remaining: 3},
		{name: "recent whitespace body", body: " \n\t", want: http.StatusOK, remaining: 3},
		{name: "stale without password", stale: true, want: http.StatusForbidden, wantError: "reauth_required", remaining: 3},
		{name: "stale correct password", stale: true, body: `{"password":"` + password + `"}`, want: http.StatusOK, remaining: 3},
		{name: "stale wrong password", stale: true, body: `{"password":"wrong-password"}`, want: http.StatusForbidden, wantError: "Passwort ist falsch", remaining: 2},
		{name: "recent account lock", accountLocked: true, want: http.StatusTooManyRequests},
		{name: "stale account lock without password", stale: true, accountLocked: true, want: http.StatusTooManyRequests},
		{name: "stale account lock with correct password", stale: true, accountLocked: true, body: `{"password":"` + password + `"}`, want: http.StatusTooManyRequests},
		{name: "recent IP pair lock", pairLocked: true, want: http.StatusTooManyRequests},
		{name: "stale IP pair lock without password", stale: true, pairLocked: true, want: http.StatusTooManyRequests},
		{name: "stale IP pair lock with correct password", stale: true, pairLocked: true, body: `{"password":"` + password + `"}`, want: http.StatusTooManyRequests},
		{name: "recent preserves previous failures", priorFailures: 2, want: http.StatusOK, remaining: 1},
		{name: "password success preserves previous failures", stale: true, body: `{"password":"` + password + `"}`, priorFailures: 2, want: http.StatusOK, remaining: 1},
		{name: "step up prompt preserves previous failures", stale: true, priorFailures: 2, want: http.StatusForbidden, wantError: "reauth_required", remaining: 1},
		{name: "wrong password consumes final attempt", stale: true, body: `{"password":"wrong-password"}`, priorFailures: 2, want: http.StatusForbidden, remaining: 0},
		{name: "foreign credential", otherOwner: true, priorFailures: 2, want: http.StatusNotFound, remaining: 1},
		{name: "missing credential", missing: true, priorFailures: 2, want: http.StatusNotFound, remaining: 1},
		{name: "last passkey protected", passkeyOnly: true, lastKey: true, priorFailures: 2, want: http.StatusConflict, remaining: 1},
		{name: "passkey only foreign credential", passkeyOnly: true, lastKey: true, otherOwner: true, want: http.StatusNotFound, remaining: 3},
		{name: "passkey only second credential removable", passkeyOnly: true, want: http.StatusOK, remaining: 3},
		{name: "passkey only stale session", passkeyOnly: true, stale: true, want: http.StatusForbidden, wantError: "reauth_required", remaining: 3},
		{name: "malformed body with recent session", body: `{"password":`, want: http.StatusBadRequest, remaining: 3},
		{name: "trailing JSON with recent session", body: `{} {}`, want: http.StatusBadRequest, remaining: 3},
		{name: "disabled account", disabled: true, want: http.StatusUnauthorized, remaining: 3},
		{name: "unauthenticated request", noSession: true, want: http.StatusUnauthorized, remaining: 3},
		{name: "missing CSRF", noCSRF: true, want: http.StatusForbidden, wantError: "invalid CSRF token", remaining: 3},
		{name: "expired session", expired: true, want: http.StatusUnauthorized, remaining: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			wa, err := auth.NewWebAuthnService(f.h.Pool, "example.com", "Example", []string{"https://example.com"})
			if err != nil {
				t.Fatal(err)
			}
			f.ah.WebAuthn = wa
			f.ah.PasskeyOnly = tc.passkeyOnly
			f.ah.Limiter = auth.NewLoginLimiter(3, time.Minute, time.Minute)
			f.ah.UserLimiter = auth.NewStickyLoginLimiter(3, time.Minute, time.Minute)
			username := fmt.Sprintf("Delete-Passkey-%d", time.Now().UnixNano())
			uid := f.createUser(t, username, password)
			otherUID := f.createUser(t, username+"-other", "other-account-password")
			credential := insertDeletionPasskey(t, f, uid)
			foreignCredential := insertDeletionPasskey(t, f, otherUID)
			ownCount := 1
			if !tc.lastKey {
				insertDeletionPasskey(t, f, uid)
				ownCount++
			}

			loginResponse := httptest.NewRecorder()
			if err := f.mgr.CreateSession(t.Context(), loginResponse,
				httptest.NewRequest(http.MethodPost, "/login", nil), uid); err != nil {
				t.Fatal(err)
			}
			if tc.stale {
				if _, err := f.h.Pool.Exec(t.Context(),
					`UPDATE sessions SET created_at = now() - interval '1 hour' WHERE user_id=$1`, uid); err != nil {
					t.Fatal(err)
				}
			}
			if tc.expired {
				if _, err := f.h.Pool.Exec(t.Context(),
					`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE user_id=$1`, uid); err != nil {
					t.Fatal(err)
				}
			}
			if tc.disabled {
				if _, err := f.h.Pool.Exec(t.Context(), `UPDATE users SET disabled=true WHERE id=$1`, uid); err != nil {
					t.Fatal(err)
				}
			}
			target := credential
			if tc.otherOwner {
				target = foreignCredential
			} else if tc.missing {
				target = 9223372036854775807
			}
			id := strconv.FormatInt(target, 10)
			r := httptest.NewRequest(http.MethodDelete, "/api/passkeys/"+id, strings.NewReader(tc.body))
			r.SetPathValue("id", id)
			const ip = "192.0.2.19"
			r.RemoteAddr = ip + ":1234"
			r.Header.Set("Content-Type", "application/json")
			for _, cookie := range loginResponse.Result().Cookies() {
				if cookie.Name == auth.SessionCookie && !tc.noSession {
					r.AddCookie(cookie)
				}
				if cookie.Name == auth.CSRFCookie && !tc.noCSRF {
					r.Header.Set(auth.CSRFHeader, cookie.Value)
				}
			}
			pairKey, userKey := strings.ToLower(username)+"|"+ip, strings.ToLower(username)
			for i := 0; i < tc.priorFailures; i++ {
				f.ah.Limiter.RecordFailure(pairKey)
				f.ah.UserLimiter.RecordFailure(userKey)
			}
			for i := 0; i < 3; i++ {
				if tc.pairLocked {
					f.ah.Limiter.RecordFailure(pairKey)
				}
				if tc.accountLocked {
					// Account-wide failures from another IP must still apply.
					f.ah.UserLimiter.RecordFailure(userKey)
				}
			}
			w := httptest.NewRecorder()
			f.mgr.RequireAuth(http.HandlerFunc(f.ah.DeletePasskey)).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.wantError != "" && !strings.Contains(w.Body.String(), tc.wantError) {
				t.Errorf("missing error %q: %s", tc.wantError, w.Body.String())
			}
			if tc.want == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
				t.Error("lockout must return Retry-After")
			}
			wantAudit := 0
			if tc.want == http.StatusOK {
				ownCount--
				wantAudit = 1
			}
			var gotOwn, gotForeign, auditCount int
			if err := f.h.Pool.QueryRow(t.Context(),
				`SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, uid).Scan(&gotOwn); err != nil {
				t.Fatal(err)
			}
			if err := f.h.Pool.QueryRow(t.Context(),
				`SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, otherUID).Scan(&gotForeign); err != nil {
				t.Fatal(err)
			}
			if gotOwn != ownCount || gotForeign != 1 {
				t.Errorf("credential rows: own=%d (want %d), foreign=%d (want 1)", gotOwn, ownCount, gotForeign)
			}
			if err := f.h.Pool.QueryRow(t.Context(),
				`SELECT count(*) FROM audit_log WHERE entity='passkey' AND entity_id=$1 AND action='delete'`,
				target).Scan(&auditCount); err != nil {
				t.Fatal(err)
			}
			if auditCount != wantAudit {
				t.Errorf("delete audit entries = %d, want %d", auditCount, wantAudit)
			}
			if !tc.pairLocked && !tc.accountLocked {
				assertDeletePasskeyBudget(t, f.ah, username, ip, tc.remaining)
			}
		})
	}
}

func insertDeletionPasskey(t *testing.T, f *authFixture, userID int64) int64 {
	t.Helper()
	var id int64
	if err := f.h.Pool.QueryRow(t.Context(),
		`INSERT INTO webauthn_credentials (user_id, credential_id, public_key, name)
		 VALUES ($1,$2,$3,$4) RETURNING id`, userID,
		[]byte(fmt.Sprintf("delete-key-%d-%d", userID, time.Now().UnixNano())), []byte{1}, "test key").Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.h.Pool.Exec(context.Background(), `DELETE FROM webauthn_credentials WHERE id=$1`, id); err != nil {
			t.Errorf("cleanup credential: %v", err)
		}
	})
	return id
}
