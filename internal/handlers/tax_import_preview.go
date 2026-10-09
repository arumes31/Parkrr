package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxTaxImportBody       = 64 << 10
	maxTaxImportEntries    = 100
	maxTaxImportTextBytes  = 1000
	maxTaxImportPropertyID = int64(9_007_199_254_740_991)
)

var taxImportExternalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

type taxImportRequest struct {
	Version    int                     `json:"version"`
	Year       int                     `json:"year"`
	PropertyID int64                   `json:"property_id"`
	Source     string                  `json:"source"`
	Reviewer   string                  `json:"reviewer"`
	Entries    []taxImportEntryRequest `json:"entries"`
}

type taxImportEntryRequest struct {
	ExternalID  string `json:"external_id"`
	Kind        string `json:"kind"`
	AmountEUR   string `json:"amount_eur"`
	Description string `json:"description"`
}

type taxImportEntry struct {
	ExternalID  string `json:"external_id"`
	Kind        string `json:"kind"`
	AmountEUR   string `json:"amount_eur"`
	AmountCents int64  `json:"amount_cents"`
	Description string `json:"description"`
}

type taxImportTotals struct {
	IncomeAdjustment     int64 `json:"income_adjustment"`
	ExpenseAdjustment    int64 `json:"expense_adjustment"`
	ExternalDepreciation int64 `json:"external_depreciation"`
}

type taxImportResponse struct {
	Preview     bool             `json:"preview"`
	Persisted   bool             `json:"persisted"`
	Verified    bool             `json:"verified"`
	Version     int              `json:"version"`
	Year        int              `json:"year"`
	PropertyID  int64            `json:"property_id"`
	Source      string           `json:"source"`
	Reviewer    string           `json:"reviewer"`
	Entries     []taxImportEntry `json:"entries"`
	TotalsCents taxImportTotals  `json:"totals_cents"`
	Notes       []string         `json:"notes"`
}

// TaxImportPreview validates externally prepared annual entries without reading
// or writing the ledger. Admin authorization and CSRF belong to the route.
func (h *Handler) TaxImportPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "Import-Vorschau bitte als POST anfordern.")
		return
	}
	var req taxImportRequest
	if err := decodeTaxPreview(
		w,
		r,
		&req,
		maxTaxImportBody,
	); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "Die Import-Datei darf höchstens 64 KiB groß sein.")
			return
		}
		writeError(w, http.StatusBadRequest, "Ungültige JSON-Importdatei; Feldnamen und Datentypen prüfen.")
		return
	}
	rep, err := buildTaxImportPreview(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func buildTaxImportPreview(req taxImportRequest) (taxImportResponse, error) {
	rep := taxImportResponse{
		Preview: true, Version: req.Version, Year: req.Year, PropertyID: req.PropertyID,
		Entries: make([]taxImportEntry, 0, len(req.Entries)),
		Notes: []string{
			"Nur Vorschau: keine Speicherung oder automatische Anwendung auf Zahlungen, Steuerbuch oder Berichte.",
			"Objekt-ID, Quelle und prüfende Person sind externe Angaben und wurden nicht verifiziert.",
			"Externe IDs sind groß-/kleinschreibungsabhängig und werden nur innerhalb dieser Datei auf Duplikate geprüft, " +
				"nicht gegen vorhandene Daten oder frühere Vorschauen.",
			"Keine steuerliche Prüfung, AfA-Berechnung oder E1b-Zuordnung; Summen sind ausschließlich externe Werte je Art.",
		},
	}
	if req.Version != 1 {
		return rep, errors.New("version muss 1 sein")
	}
	if req.Year != 2025 && req.Year != 2026 {
		return rep, errors.New("jahr muss 2025 oder 2026 sein")
	}
	if req.PropertyID <= 0 || req.PropertyID > maxTaxImportPropertyID {
		return rep, errors.New("property_id muss eine positive, in JavaScript exakt darstellbare Ganzzahl sein")
	}
	if len(req.Entries) < 1 || len(req.Entries) > maxTaxImportEntries {
		return rep, errors.New("die Importdatei muss 1 bis 100 Einträge enthalten")
	}
	var err error
	rep.Source, err = taxImportText(req.Source)
	if err != nil {
		return rep, fmt.Errorf("quelle: %w", err)
	}
	rep.Reviewer, err = taxImportText(req.Reviewer)
	if err != nil {
		return rep, fmt.Errorf("prüfende Person: %w", err)
	}
	seen := make(map[string]bool, len(req.Entries))
	for i, entry := range req.Entries {
		if !taxImportExternalID.MatchString(entry.ExternalID) || seen[entry.ExternalID] {
			return rep, fmt.Errorf(
				"eintrag %d: external_id muss eindeutig sein und dem Format [A-Za-z0-9][A-Za-z0-9._:-]{0,63} entsprechen",
				i+1,
			)
		}
		seen[entry.ExternalID] = true
		normalized, err := normalizeTaxImportEntry(entry)
		if err != nil {
			return rep, fmt.Errorf("eintrag %d: %w", i+1, err)
		}
		switch normalized.Kind {
		case "income_adjustment":
			rep.TotalsCents.IncomeAdjustment += normalized.AmountCents
		case "expense_adjustment":
			rep.TotalsCents.ExpenseAdjustment += normalized.AmountCents
		case "external_depreciation":
			rep.TotalsCents.ExternalDepreciation += normalized.AmountCents
		}
		rep.Entries = append(rep.Entries, normalized)
	}
	// At most 100 bounded entries cannot overflow int64; check final sums so
	// opposite corrections give the same result regardless of their file order.
	for _, total := range []int64{
		rep.TotalsCents.IncomeAdjustment,
		rep.TotalsCents.ExpenseAdjustment,
		rep.TotalsCents.ExternalDepreciation,
	} {
		if total < -maxTaxReviewCents || total > maxTaxReviewCents {
			return rep, errors.New("die Summe je Art darf im Betrag 1000000000.00 EUR nicht überschreiten")
		}
	}
	return rep, nil
}

func normalizeTaxImportEntry(req taxImportEntryRequest) (taxImportEntry, error) {
	out := taxImportEntry{ExternalID: req.ExternalID, Kind: req.Kind}
	switch req.Kind {
	case "income_adjustment", "expense_adjustment", "external_depreciation":
	default:
		return out, errors.New("kind muss income_adjustment, expense_adjustment oder external_depreciation sein")
	}
	var err error
	out.Description, err = taxImportText(req.Description)
	if err != nil {
		return out, fmt.Errorf("beschreibung: %w", err)
	}
	value := req.AmountEUR
	isNegative := strings.HasPrefix(value, "-")
	if isNegative {
		if req.Kind == "external_depreciation" {
			return out, errors.New("externe AfA darf kein negatives Vorzeichen haben")
		}
		value = strings.TrimPrefix(value, "-")
	}
	cents, err := taxReviewCents(&value)
	if err != nil {
		return out, fmt.Errorf("betrag: %w", err)
	}
	out.AmountCents = *cents
	out.AmountEUR = fmt.Sprintf("%d.%02d", *cents/100, *cents%100)
	if isNegative && *cents != 0 {
		out.AmountCents = -*cents
		out.AmountEUR = "-" + out.AmountEUR
	}
	return out, nil
}

func taxImportText(value string) (string, error) {
	// Check before trimming: surrounding line breaks must not disappear silently.
	hasForbiddenRune := strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) >= 0
	if len(value) > maxTaxImportTextBytes || hasForbiddenRune {
		return "", errors.New("text muss einzeilig, ohne Steuerzeichen und höchstens 1000 UTF-8-Bytes lang sein")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("text ist erforderlich")
	}
	return value, nil
}
