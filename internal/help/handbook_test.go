package help

import (
	"os"
	"strings"
	"testing"
)

// Die eingebettete Kopie muss dem Original in docs/ entsprechen. Ein Windows-Checkout
// mit autocrlf liefert CRLF; verglichen wird deshalb zeilenweise.
func TestHandbookMatchesDocs(t *testing.T) {
	src, err := os.ReadFile("../../docs/betreiber-handbuch.md")
	if err != nil {
		t.Fatalf("read docs/betreiber-handbuch.md: %v", err)
	}
	norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if norm(string(src)) != norm(Handbook) {
		t.Fatal("internal/help/betreiber-handbuch.md weicht von docs/betreiber-handbuch.md ab: " +
			"cp docs/betreiber-handbuch.md internal/help/betreiber-handbuch.md")
	}
}
