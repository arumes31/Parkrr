// Package help bettet das Betreiber-Handbuch für die Seite "Hilfe & Handbuch" ein.
//
// betreiber-handbuch.md ist eine Kopie von docs/betreiber-handbuch.md. Die Quelle
// bleibt docs/; die Kopie liegt hier, weil .dockerignore /docs aus dem Build-Kontext
// hält (Berichte können Demo-Datensätze und Bildschirmfotos enthalten).
// TestHandbookMatchesDocs schlägt fehl, sobald beide auseinanderlaufen; dann die
// Datei aus docs/ hierher kopieren.
package help

import _ "embed"

// Handbook ist das Betreiber-Handbuch als Markdown.
//
//go:embed betreiber-handbuch.md
var Handbook string
