package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Die App verlinkt aus Backup und Steuerjahr auf Abschnitte des Handbuchs. Fehlt
// eine dieser Überschriften, landet der Link am Seitenanfang — der Test hält die
// Überschriften und die eingebettete Datei zusammen.
func TestHandbookServesEmbeddedMarkdown(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	h.Handbook(w, httptest.NewRequest(http.MethodGet, "/api/help/handbook", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body struct {
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Ein Windows-Checkout mit autocrlf liefert CRLF; verglichen wird zeilenweise.
	md := strings.ReplaceAll(body.Markdown, "\r\n", "\n")
	for _, heading := range []string{
		"# Parkrr — Betreiber-Handbuch",
		"## 2. Backups: einrichten, prüfen, wiederherstellen",
		"### Steuerbereich: zuerst nur lesen, später aktivieren",
	} {
		if !strings.Contains(md, "\n"+heading+"\n") && !strings.HasPrefix(md, heading+"\n") {
			t.Errorf("handbook lacks heading %q", heading)
		}
	}
}
