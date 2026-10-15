// Package docs bettet das Betreiber-Handbuch in die Anwendung ein. Die Datei bleibt
// die eine Quelle: README und Repository verlinken sie, die App liefert sie unter
// "Hilfe & Handbuch" aus. Eine Kopie unter web/static würde früher oder später
// vom Original abweichen.
//
// .dockerignore schließt /docs und *.md aus (Berichte können Demo-Datensätze und
// Bildschirmfotos enthalten); genau diese beiden Dateien sind dort ausgenommen.
package docs

import _ "embed"

// Handbook ist das Betreiber-Handbuch als Markdown.
//
//go:embed betreiber-handbuch.md
var Handbook string
