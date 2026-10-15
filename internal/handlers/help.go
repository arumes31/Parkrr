package handlers

import (
	"net/http"

	"github.com/preining/parkrr/internal/help"
)

// Handbook liefert das eingebettete Betreiber-Handbuch für die Seite
// "Hilfe & Handbuch". Nur für Admins: es richtet sich an den Betrieb (Backups,
// Schalter, Wiederherstellung), nicht an Bearbeiter oder Nur-Leser.
func (h *Handler) Handbook(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"markdown": help.Handbook})
}
