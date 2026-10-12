package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireLogoutWithoutDatabase(t *testing.T) {
	// A nil pool proves cookie-free requests and rejected CSRF never access DB.
	m := &Manager{}
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		header string
		want   int
	}{
		{name: "no cookie", want: http.StatusOK},
		{name: "empty cookie", cookie: &http.Cookie{Name: SessionCookie}, want: http.StatusOK},
		{name: "missing csrf", cookie: &http.Cookie{Name: SessionCookie, Value: "test-session"}, want: http.StatusForbidden},
		{name: "wrong csrf", cookie: &http.Cookie{Name: SessionCookie, Value: "test-session"}, header: "wrong", want: http.StatusForbidden},
		{name: "csrf for another session", cookie: &http.Cookie{Name: SessionCookie, Value: "test-session"}, header: m.csrfToken("other-session"), want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler := m.RequireLogout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if err := m.DestroySession(r.Context(), w, r); err != nil {
					t.Fatal(err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			req.Header.Set(CSRFHeader, tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want || reached != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d reached=%v; want status=%d", rec.Code, reached, tc.want)
			}
			if tc.want == http.StatusForbidden && len(rec.Result().Cookies()) != 0 {
				t.Fatal("rejected logout discarded cookies needed for retry")
			}
		})
	}
}
