package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxTaxReviewBody  = 32 << 10
	maxTaxReviewCents = int64(100_000_000_000) // EUR 1 billion, exact in JSON number consumers.
	taxReviewKULimit  = int64(5_500_000)
	taxReviewKUUpper  = int64(6_050_000)
	taxReviewKUSource = "https://www.usp.gv.at/themen/steuern-finanzen/umsatzsteuer-ueberblick/" +
		"weitere-informationen-zur-umsatzsteuer/weitere-steuertatbestaende-und-befreiungen/kleinunternehmen.html"
	taxReviewLaw19 = "https://www.ris.bka.gv.at/eli/bgbl/1988/400/P19/NOR40246107"
	taxReviewEStR  = "https://findok.bmf.gv.at/findok/resources/pdf/" +
		"84e8cc42-775a-415f-8160-7c2e1deb822c/19973.4.-1.X.pdf"
)

type taxReviewRequest struct {
	Year      int                       `json:"year"`
	Turnover  *taxReviewTurnoverRequest `json:"turnover"`
	YearShift *taxReviewShiftRequest    `json:"year_shift"`
}

type taxReviewTurnoverRequest struct {
	CurrentEUR  *string `json:"current_eur"`
	PreviousEUR *string `json:"previous_eur"`
	ReceiptsEUR *string `json:"receipts_eur"`
	OptedOut    *bool   `json:"opted_out"`
	Source      string  `json:"source"`
	Reviewer    string  `json:"reviewer"`
}

type taxReviewShiftRequest struct {
	Kind               string `json:"kind"`
	EconomicYear       *int   `json:"economic_year"`
	PaidOn             string `json:"paid_on"`
	DueOn              string `json:"due_on"`
	RegularlyRecurring *bool  `json:"regularly_recurring"`
	Source             string `json:"source"`
	Reviewer           string `json:"reviewer"`
}

type taxReviewResponse struct {
	Preview   bool                `json:"preview"`
	Persisted bool                `json:"persisted"`
	Year      int                 `json:"year"`
	Turnover  taxReviewTurnover   `json:"turnover"`
	YearShift *taxReviewYearShift `json:"year_shift,omitempty"`
	Notes     []string            `json:"notes"`
}

type taxReviewTurnover struct {
	Status             string   `json:"status"`
	CurrentCents       *int64   `json:"current_cents"`
	PreviousCents      *int64   `json:"previous_cents"`
	ReceiptsCents      *int64   `json:"receipts_cents"`
	DifferenceCents    *int64   `json:"difference_cents"`
	OptedOut           *bool    `json:"opted_out"`
	LimitCents         int64    `json:"limit_cents"`
	ToleranceCents     int64    `json:"tolerance_cents"`
	PreviousYear       int      `json:"previous_year"`
	ExternallySupplied bool     `json:"externally_supplied"`
	Verified           bool     `json:"verified"`
	Source             string   `json:"source"`
	Reviewer           string   `json:"reviewer"`
	RuleSource         string   `json:"rule_source"`
	RuleCheckedOn      string   `json:"rule_checked_on"`
	Notes              []string `json:"notes"`
}

type taxReviewYearShift struct {
	Status             string   `json:"status"`
	Kind               string   `json:"kind"`
	EconomicYear       *int     `json:"economic_year"`
	PaymentYear        *int     `json:"payment_year"`
	SuggestedYear      *int     `json:"suggested_year,omitempty"`
	PaidOn             string   `json:"paid_on"`
	DueOn              string   `json:"due_on"`
	RegularlyRecurring *bool    `json:"regularly_recurring"`
	WindowFrom         string   `json:"window_from,omitempty"`
	WindowTo           string   `json:"window_to,omitempty"`
	ExternallySupplied bool     `json:"externally_supplied"`
	Verified           bool     `json:"verified"`
	Source             string   `json:"source"`
	Reviewer           string   `json:"reviewer"`
	RuleSources        []string `json:"rule_sources"`
	Notes              []string `json:"notes"`
}

// TaxReviewPreview evaluates caller-supplied figures without reading or changing
// the ledger. Authentication/authorization belongs to the registered route.
func (h *Handler) TaxReviewPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "Vorschau bitte als POST anfordern.")
		return
	}
	var req taxReviewRequest
	if err := decodeTaxReview(w, r, &req); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "Die Vorschauanfrage ist zu groß.")
			return
		}
		writeError(w, http.StatusBadRequest, "Ungültige JSON-Vorschauanfrage; Feldnamen und Datentypen prüfen.")
		return
	}
	rep, err := buildTaxReview(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func decodeTaxReview(w http.ResponseWriter, r *http.Request, req *taxReviewRequest) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || mt != "application/json" {
			return errNotJSON
		}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTaxReviewBody))
	if err != nil {
		return fmt.Errorf("read tax preview: %w", err)
	}
	if !utf8.Valid(raw) {
		return errors.New("tax preview must contain valid utf-8")
	}
	// Ambiguous repeated keys must not silently replace a financial input.
	check := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueTaxReviewFields(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); !errors.Is(err, io.EOF) {
		return errors.New("tax preview requires one json value")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return fmt.Errorf("decode tax preview: %w", err)
	}
	return nil
}

func uniqueTaxReviewFields(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("tax preview nesting is too deep")
	}
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("read tax preview token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		if delim == '{' {
			key, err := dec.Token()
			if err != nil {
				return fmt.Errorf("read tax preview key: %w", err)
			}
			name, ok := key.(string)
			// encoding/json accepts case-insensitive aliases; our financial input
			// contract deliberately requires the canonical lowercase spelling.
			invalidName := strings.IndexFunc(name, func(r rune) bool {
				return r != '_' && (r < 'a' || r > 'z')
			}) >= 0
			if !ok || invalidName || seen[name] {
				return errors.New("duplicate or invalid tax preview field")
			}
			seen[name] = true
		}
		if err := uniqueTaxReviewFields(dec, depth+1); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("close tax preview value: %w", err)
	}
	return nil
}

func buildTaxReview(req taxReviewRequest) (taxReviewResponse, error) {
	rep := taxReviewResponse{
		Preview: true, Year: req.Year,
		Notes: []string{
			"Unverbindliche Prüfung externer Angaben; keine Bestätigung der steuerlichen Berechtigung oder Zuordnung.",
			"Es werden keine Buchungen, Rechnungen, Steuerdaten oder Einstellungen geändert oder gespeichert.",
		},
	}
	if req.Year < 2025 || req.Year > 2026 {
		return rep, errors.New("die Vorschau unterstützt nur die geprüften Regeljahre 2025 und 2026")
	}
	var err error
	rep.Turnover, err = reviewTaxTurnover(req.Year, req.Turnover)
	if err != nil {
		return rep, err
	}
	if req.YearShift != nil {
		shift, err := reviewTaxYearShift(req.Year, *req.YearShift)
		if err != nil {
			return rep, err
		}
		rep.YearShift = &shift
	}
	return rep, nil
}

// Decimal EUR input is parsed directly into cents; rounding, exponent notation
// and locale-dependent grouping would silently alter a threshold comparison.
func taxReviewCents(value *string) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	s := *value
	if len(s) == 0 || len(s) > 13 {
		return nil, errors.New("betrag muss eine EUR-Dezimalzeichenfolge bis 1000000000.00 sein")
	}
	whole, fraction, hasPoint := strings.Cut(s, ".")
	invalidFraction := hasPoint && (len(fraction) < 1 || len(fraction) > 2)
	if whole == "" || invalidFraction {
		return nil, errors.New("betrag braucht Ziffern und höchstens zwei Nachkommastellen mit Dezimalpunkt")
	}
	for _, part := range []string{whole, fraction} {
		for i := range len(part) {
			if part[i] < '0' || part[i] > '9' {
				return nil, errors.New("betrag darf nur Ziffern und einen Dezimalpunkt enthalten")
			}
		}
	}
	if len(fraction) == 1 {
		fraction += "0"
	}
	if fraction == "" {
		fraction = "00"
	}
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents > maxTaxReviewCents {
		return nil, errors.New("betrag überschreitet 1000000000.00 EUR")
	}
	return &cents, nil
}

func taxReviewProvenance(source, reviewer string, required bool) (string, string, error) {
	source, reviewer = strings.TrimSpace(source), strings.TrimSpace(reviewer)
	if required && (source == "" || reviewer == "") {
		return "", "", errors.New("quelle und prüfende Person sind für externe Angaben erforderlich")
	}
	for _, s := range []string{source, reviewer} {
		if len(s) > 1000 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return "", "", errors.New("quelle und prüfende Person müssen kurze einzeilige Texte sein")
		}
	}
	return source, reviewer, nil
}

func reviewTaxTurnover(year int, req *taxReviewTurnoverRequest) (taxReviewTurnover, error) {
	out := taxReviewTurnover{
		Status: "unknown", LimitCents: taxReviewKULimit, ToleranceCents: taxReviewKUUpper,
		PreviousYear: year - 1, RuleSource: taxReviewKUSource, RuleCheckedOn: "2026-10-08",
		Notes: []string{
			"Maßgebliche ausgeführte Inlandsumsätze des gesamten Unternehmens extern ermitteln; " +
				"Zahlungseingänge sind nur ein Vergleichswert.",
			"Der Zahlenvergleich prüft weder Unternehmenssitz noch Ausnahmen, Vollständigkeit oder Wirksamkeit eines Verzichts.",
		},
	}
	if req == nil {
		out.Notes = append(out.Notes, "Jahresumsatz, Vorjahresumsatz und Verzichtsstatus sind unbekannt.")
		return out, nil
	}
	var err error
	out.CurrentCents, err = taxReviewCents(req.CurrentEUR)
	if err != nil {
		return out, fmt.Errorf("jahresumsatz: %w", err)
	}
	out.PreviousCents, err = taxReviewCents(req.PreviousEUR)
	if err != nil {
		return out, fmt.Errorf("vorjahresumsatz: %w", err)
	}
	out.ReceiptsCents, err = taxReviewCents(req.ReceiptsEUR)
	if err != nil {
		return out, fmt.Errorf("zahlungseingänge: %w", err)
	}
	out.OptedOut = req.OptedOut
	out.ExternallySupplied = req.CurrentEUR != nil || req.PreviousEUR != nil || req.OptedOut != nil
	out.Source, out.Reviewer, err = taxReviewProvenance(req.Source, req.Reviewer, out.ExternallySupplied)
	if err != nil {
		return out, err
	}
	if out.CurrentCents != nil && out.ReceiptsCents != nil {
		delta := *out.CurrentCents - *out.ReceiptsCents
		out.DifferenceCents = &delta
	}
	if out.ReceiptsCents != nil {
		out.Notes = append(out.Notes,
			"Zahlungseingänge wurden vom Aufrufer übermittelt und nicht gegen das Steuerbuch geprüft.")
	}
	missingTurnover := out.CurrentCents == nil || out.PreviousCents == nil
	if missingTurnover || req.OptedOut == nil {
		out.Notes = append(out.Notes,
			"Mindestens Jahresumsatz, Vorjahresumsatz oder Verzichtsstatus fehlt; unbekannt ist nicht null.")
	}
	switch {
	case req.OptedOut != nil && *req.OptedOut:
		out.Status = "opted_out"
		out.Notes = append(out.Notes, "Angegeben ist ein Verzicht auf die Befreiung; die Umsatzgrenze hebt diesen nicht auf.")
	case out.PreviousCents != nil && *out.PreviousCents > taxReviewKULimit:
		out.Status = "previous_limit_exceeded"
		out.Notes = append(out.Notes,
			"Der angegebene Vorjahresumsatz überschreitet 55.000 EUR; "+
				"die laufende 10-%-Toleranz beseitigt dies nicht.")
	case missingTurnover || req.OptedOut == nil:
		// A missing input cannot produce an affirmative threshold conclusion.
	case *out.CurrentCents <= taxReviewKULimit:
		out.Status = "within_limit"
		out.Notes = append(out.Notes,
			"Die angegebenen Jahreswerte liegen innerhalb der Umsatzgrenzen; "+
				"weitere Voraussetzungen bleiben ungeprüft.")
	case *out.CurrentCents <= taxReviewKUUpper:
		out.Status = "within_tolerance"
		out.Notes = append(out.Notes,
			"Laufender Umsatz liegt in der 10-%-Toleranz; "+
				"im Folgejahr wäre die 55.000-EUR-Vorjahresgrenze überschritten.")
	default:
		out.Status = "current_limit_exceeded"
		out.Notes = append(out.Notes,
			"Die 10-%-Toleranz ist überschritten. Der betroffene Überschreitungsumsatz "+
				"und sein Datum sind aus einer Jahressumme nicht bestimmbar.")
	}
	return out, nil
}

func reviewTaxYearShift(year int, req taxReviewShiftRequest) (taxReviewYearShift, error) {
	out := taxReviewYearShift{
		Status: "unknown", Kind: req.Kind, EconomicYear: req.EconomicYear,
		PaidOn: req.PaidOn, DueOn: req.DueOn, RegularlyRecurring: req.RegularlyRecurring,
		RuleSources: []string{taxReviewLaw19, taxReviewEStR},
		Notes: []string{
			"Nur ein Prüfkandidat nach einem 15-Tage-Fenstermodell, keine bestätigte steuerliche Jahreszuordnung.",
			"§ 19 EStG nennt kurze Zeit; 15 Tage und Fälligkeit am Jahreswechsel folgen der Verwaltungsauslegung " +
				"(EStR Rz 4630–4632, verlinkte Archivfassung 2007). Anwendbarkeit fachlich prüfen.",
			"Eine regelmäßige Zahlung ist nicht allein durch Ratenzahlung oder ein Periodenetikett belegt. " +
				"Es werden keine Originalvorgänge geändert.",
		},
	}
	if req.Kind != "income" && req.Kind != "expense" {
		return out, errors.New("jahreswechsel: kind muss income oder expense sein")
	}
	hasDates := req.PaidOn != "" || req.DueOn != ""
	out.ExternallySupplied = req.EconomicYear != nil || hasDates || req.RegularlyRecurring != nil
	var err error
	out.Source, out.Reviewer, err = taxReviewProvenance(req.Source, req.Reviewer, out.ExternallySupplied)
	if err != nil {
		return out, err
	}
	parseDate := func(value string) (*time.Time, error) {
		if value == "" {
			return nil, nil
		}
		d, err := time.Parse(dateLayout, value)
		if err != nil || len(value) != 10 {
			return nil, errors.New("datum muss ein gültiges YYYY-MM-DD sein")
		}
		if d.Year() < year-1 || d.Year() > year+1 {
			return nil, errors.New("datum muss im Prüfjahr oder einem angrenzenden Kalenderjahr liegen")
		}
		return &d, nil
	}
	paid, err := parseDate(req.PaidOn)
	if err != nil {
		return out, fmt.Errorf("zahlungsdatum: %w", err)
	}
	due, err := parseDate(req.DueOn)
	if err != nil {
		return out, fmt.Errorf("vertragsfälligkeit: %w", err)
	}
	if paid != nil {
		y := paid.Year()
		out.PaymentYear = &y
	}
	if req.EconomicYear != nil && (*req.EconomicYear < 2025 || *req.EconomicYear > 2026) {
		return out, errors.New("wirtschaftliches Jahr muss 2025 oder 2026 sein")
	}
	if req.EconomicYear != nil && paid != nil {
		if *req.EconomicYear != year && paid.Year() != year {
			return out, errors.New("jahreswechsel muss das angeforderte Prüfjahr betreffen")
		}
	}
	missingDates := paid == nil || due == nil
	missingAttribution := req.EconomicYear == nil || req.RegularlyRecurring == nil
	if missingDates || missingAttribution {
		out.Notes = append(out.Notes,
			"Zahlungsdatum, Vertragsfälligkeit, wirtschaftliches Jahr und Regelmäßigkeit müssen einzeln belegt werden.")
		return out, nil
	}
	if !*req.RegularlyRecurring {
		out.Status = "not_recurring"
		return out, nil
	}
	if paid.Year() == *req.EconomicYear {
		out.Status = "same_year"
		return out, nil
	}
	// Both dates must lie at the SAME boundary between payment and economic year.
	boundary := *req.EconomicYear
	if paid.Year() > boundary {
		boundary = paid.Year()
	}
	from := time.Date(
		boundary-1,
		time.December,
		17,
		0, 0, 0, 0, // Midnight UTC; no local timezone or daylight-saving ambiguity.
		time.UTC,
	)
	to := time.Date(
		boundary,
		time.January,
		15,
		0, 0, 0, 0, // Matching midnight precision for date-only inputs.
		time.UTC,
	)
	out.WindowFrom, out.WindowTo = from.Format(dateLayout), to.Format(dateLayout)
	distance := paid.Year() - *req.EconomicYear
	isAdjacent := distance == -1 || distance == 1
	paidInWindow := !paid.Before(from) && !paid.After(to)
	dueInWindow := !due.Before(from) && !due.After(to)
	if !isAdjacent || !paidInWindow || !dueInWindow {
		out.Status = "outside_window"
		return out, nil
	}
	out.Status = "candidate"
	y := *req.EconomicYear
	out.SuggestedYear = &y
	return out, nil
}
