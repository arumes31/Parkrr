package handlers

import (
	"net/http"

	"github.com/preining/parkrr/docs"
)

// Handbook liefert das eingebettete Betreiber-Handbuch für die Seite
// "Hilfe & Handbuch". Es enthält keine Betriebsdaten, nur die Anleitung aus dem
// Repository; der Zugang folgt trotzdem der Anmeldung wie jede andere App-Seite.
func (h *Handler) Handbook(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"markdown": docs.Handbook})
}
