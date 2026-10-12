package handlers

import (
	"net/http"
	"strings"
	"time"
)

// ReadinessConfig contains non-secret runtime facts used by the admin checklist.
type ReadinessConfig struct {
	SMTPFromConfigured bool
	Require2FA         bool
	WebAuthnConfigured bool
	TimeZone           string
	AutoInvoiceCron    string
}

type readinessItem struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Href     string `json:"href"`
	Critical bool   `json:"critical"`
}

type readinessResponse struct {
	State     string          `json:"state"`
	CheckedAt time.Time       `json:"checked_at"`
	Items     []readinessItem `json:"items"`
}

func readinessOverall(items []readinessItem) string {
	state := "ready"
	for _, item := range items {
		if item.State == "blocked" {
			return "blocked"
		}
		if item.State == "warning" {
			state = "warning"
		}
	}
	return state
}

// OperationalReadiness returns an actionable, admin-only production checklist.
func (h *Handler) OperationalReadiness(w http.ResponseWriter, r *http.Request) {
	billing, err := h.loadBillingSettings(r.Context())
	if err != nil {
		serverError(w, r, "could not check operational readiness", err)
		return
	}
	var activeUsers, protectedUsers int
	err = h.Pool.QueryRow(r.Context(),
		`SELECT count(*), count(*) FILTER (WHERE factor_ready)
		   FROM (
		     SELECT u.id, (u.totp_enabled OR ($1 AND EXISTS (
		       SELECT 1 FROM webauthn_credentials wc WHERE wc.user_id=u.id
		     ))) AS factor_ready
		       FROM users u WHERE NOT u.disabled
		   ) users_with_factor`, h.Readiness.WebAuthnConfigured,
	).Scan(&activeUsers, &protectedUsers)
	if err != nil {
		serverError(w, r, "could not check operational readiness", err)
		return
	}
	var volumeCron, s3Cron string
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT volume_cron, s3_cron FROM backup_settings WHERE id=1`).Scan(&volumeCron, &s3Cron); err != nil {
		serverError(w, r, "could not check operational readiness", err)
		return
	}

	billingReady := strings.TrimSpace(billing.SellerName) != "" && strings.TrimSpace(billing.SellerAddress) != "" &&
		(billing.Kleinunternehmer || strings.TrimSpace(billing.SellerUID) != "")
	items := []readinessItem{
		{ID: "smtp", Label: "E-Mail-Versand", State: "ready", Reason: "SMTP und Absender sind eingerichtet.", Href: "#/settings"},
		{ID: "billing", Label: "Rechnungsdaten", State: "ready", Reason: "Pflichtangaben für Rechnungen sind vollständig.", Href: "#/billing", Critical: true},
		{ID: "timezone", Label: "Geschäftszeitzone", State: "ready", Reason: "Eine explizite IANA-Zeitzone ist gesetzt.", Href: "#/settings", Critical: true},
		{ID: "backup", Label: "Datensicherung", State: "ready", Reason: "Schlüssel und mindestens ein Sicherungsziel sind konfiguriert.", Href: "#/backup", Critical: true},
		{ID: "2fa", Label: "Zwei-Faktor-Schutz", State: "ready", Reason: "2FA ist erzwungen und alle aktiven Konten sind eingerichtet.", Href: "#/users", Critical: true},
		{ID: "auto_invoice", Label: "Automatische Rechnungen", State: "ready", Reason: "Der Rechnungslauf ist eingerichtet.", Href: "#/billing"},
	}
	if !h.Mail.Enabled() || !h.Readiness.SMTPFromConfigured {
		items[0].State = "warning"
		items[0].Reason = "PARKRR_SMTP_HOST oder PARKRR_SMTP_FROM fehlt; Erinnerungen können nicht versendet werden."
	}
	if !billingReady {
		items[1].State = "blocked"
		items[1].Reason = "Name, Anschrift oder – bei USt-Ausweis – UID fehlt."
	}
	if strings.TrimSpace(h.Readiness.TimeZone) == "" {
		items[2].State = "warning"
		items[2].Reason = "PARKRR_TIMEZONE fehlt; der Container-Standard wird verwendet."
	}
	backupConfigured := h.BackupKey != "" && (h.BackupDir != "" || h.S3.Enabled())
	backupScheduled := (h.BackupDir != "" && strings.TrimSpace(volumeCron) != "") ||
		(h.S3.Enabled() && strings.TrimSpace(s3Cron) != "")
	if !backupConfigured {
		items[3].State = "blocked"
		items[3].Reason = "Backup-Schlüssel oder Sicherungsziel fehlt."
	} else if !backupScheduled {
		items[3].State = "warning"
		items[3].Reason = "Ein Sicherungsziel ist vorhanden, aber kein Zeitplan ist aktiv."
	}
	if !h.Readiness.Require2FA || activeUsers == 0 || protectedUsers != activeUsers {
		items[4].State = "warning"
		items[4].Reason = "PARKRR_REQUIRE_2FA ist nicht aktiv oder noch nicht alle Konten sind eingerichtet."
	}
	if strings.TrimSpace(h.Readiness.AutoInvoiceCron) == "" {
		items[5].State = "warning"
		items[5].Reason = "PARKRR_AUTO_INVOICE_CRON fehlt; automatische Rechnungen sind deaktiviert."
	} else if !billingReady {
		items[5].State = "blocked"
		items[5].Reason = "Zeitplan ist aktiv, aber die Rechnungsdaten sind unvollständig."
	}

	writeJSON(w, http.StatusOK, readinessResponse{
		State: readinessOverall(items), CheckedAt: h.now(), Items: items,
	})
}
