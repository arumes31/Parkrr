package handlers

import "net/http"

// requireTaxWrite guards each mutation before request parsing or side effects.
// Keeping this server-side also protects direct API calls and receipt uploads.
func (h *Handler) requireTaxWrite(w http.ResponseWriter) bool {
	if h != nil && h.TaxWritesEnabled {
		return true
	}
	writeError(w, http.StatusForbidden,
		"Steuerbereich ist nur lesend. Änderungen müssen zuerst durch den Betreiber freigeschaltet werden.")
	return false
}
