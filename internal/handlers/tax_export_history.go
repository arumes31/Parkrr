package handlers

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var errTaxExportChanged = errors.New("payments changed while preparing tax export")

// taxPaymentHistory is documentary evidence, never another income table.
type taxPaymentHistory struct {
	Payments   bytes.Buffer
	Audit      bytes.Buffer
	Links      bytes.Buffer
	Count      int
	Reversed   int
	Unverified int
}

// buildTaxPaymentHistory reads its three linked tables from one read-only
// snapshot. The existing tax summary has separate reads; reject a changed
// payment set rather than silently joining its stale receipts to new history.
func (h *Handler) buildTaxPaymentHistory(ctx context.Context, rep taxYearReport) (taxPaymentHistory, error) {
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return taxPaymentHistory{}, fmt.Errorf("begin payment history snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	history, err := loadTaxPaymentHistory(ctx, tx, rep)
	if err != nil {
		return history, err
	}
	if err := tx.Commit(ctx); err != nil {
		return history, fmt.Errorf("finish payment history snapshot: %w", err)
	}
	return history, nil
}

func loadTaxPaymentHistory(ctx context.Context, q dbQuerier, rep taxYearReport) (taxPaymentHistory, error) {
	history, ids, err := readTaxPaymentHistory(ctx, q, rep)
	if err != nil {
		return history, err
	}
	if err := writeTaxPaymentAuditCSV(ctx, q, &history.Audit, ids); err != nil {
		return history, err
	}
	incomeIDs := make([]int64, 0, len(rep.payments))
	for _, p := range rep.payments {
		incomeIDs = append(incomeIDs, p.ID)
	}
	if err := writeTaxAllocationCSV(ctx, q, &history.Links, incomeIDs); err != nil {
		return history, fmt.Errorf("write current payment links: %w", err)
	}
	return history, nil
}

func readTaxPaymentHistory(ctx context.Context, q dbQuerier, rep taxYearReport) (taxPaymentHistory, []int64, error) {
	history := taxPaymentHistory{}
	ids := []int64{}
	income := make(map[int64]taxYearPayment, len(rep.payments))
	for _, p := range rep.payments {
		income[p.ID] = p
	}
	from := fmt.Sprintf("%04d-01-01", rep.Year)
	until := fmt.Sprintf("%04d-01-01", rep.Year+1)
	// JSON dates are compared as ISO text, not cast: malformed legacy audit
	// fields must not break an otherwise valid export. Metadata's intermediate
	// receipt years remain discoverable after another date correction.
	rows, err := q.Query(ctx, `WITH related_audit AS (
		SELECT a.entity,a.entity_id,a.changes FROM audit_log a
		WHERE (a.entity IN ('payment','payment_tax_metadata','invoice')) AND (
			(a.created_at >= $1::date AND a.created_at < $2::date)
			OR (a.entity='payment_tax_metadata' AND (
				(a.changes#>>'{received_on,old}' >= ($1::date)::text AND a.changes#>>'{received_on,old}' < ($2::date)::text)
				OR (a.changes#>>'{received_on,new}' >= ($1::date)::text AND a.changes#>>'{received_on,new}' < ($2::date)::text)))))
		SELECT p.id,p.person_id,p.paid_on,COALESCE(m.received_on,p.paid_on),p.amount,p.method,p.note,p.auto,
			COALESCE(p.settles_kind,''),COALESCE(p.settles_ref,0),COALESCE(p.settles_period,''),
			p.reversed,p.reversed_at,p.reversed_by,p.created_at,p.created_by,
			COALESCE(m.tax_property_id,(SELECT id FROM tax_properties ORDER BY is_default DESC,id LIMIT 1)),
			EXISTS(SELECT 1 FROM audit_log a WHERE a.entity='payment' AND a.entity_id=p.id
				AND jsonb_typeof(a.changes->'released_allocations')='array')
		FROM payments p LEFT JOIN payment_tax_metadata m ON m.payment_id=p.id
		WHERE p.paid_on >= $1::date AND p.paid_on < $2::date
			OR COALESCE(m.received_on,p.paid_on) >= $1::date AND COALESCE(m.received_on,p.paid_on) < $2::date
			OR p.reversed_at >= $1::date AND p.reversed_at < $2::date
			OR COALESCE(m.received_on,p.paid_on) BETWEEN make_date($3-1,12,17) AND make_date($3+1,1,15)
			OR EXISTS(SELECT 1 FROM related_audit a WHERE a.entity IN ('payment','payment_tax_metadata') AND a.entity_id=p.id)
			OR EXISTS(SELECT 1 FROM related_audit a, jsonb_array_elements(
				CASE WHEN jsonb_typeof(a.changes->'released_invoice_payments')='array'
				THEN a.changes->'released_invoice_payments' ELSE '[]'::jsonb END) link
				WHERE a.entity='invoice' AND link->>'payment_id'=p.id::text)
		ORDER BY p.id`, from, until, rep.Year)
	if err != nil {
		return history, ids, fmt.Errorf("query payment history: %w", err)
	}
	defer rows.Close()
	history.Payments.WriteString("\ufeff") // Match the package's German Excel CSV encoding.
	writer := csv.NewWriter(&history.Payments)
	writer.Comma = ';'
	if err := writer.Write([]string{
		"payment_id", "person_id", "buchungstag", "zuflussdatum_aktuell", "betrag_eur", "zahlungsart", "notiz",
		"ueber_schalter", "settles_kind", "settles_ref", "periode", "korrigiert", "korrigiert_am", "korrigiert_von",
		"erstellt_am", "erstellt_von", "steuerobjekt_id_aktuell", "in_einnahmen_csv", "ruecknahme_nachweis",
	}); err != nil {
		return history, ids, fmt.Errorf("write payment history header: %w", err)
	}
	matched := 0
	for rows.Next() {
		var p taxYearPayment
		var note string
		var refID int64
		var reversed, hasReleasedLinks bool
		var reversedAt *time.Time
		var reversedBy, createdBy *int64
		var createdAt time.Time
		if err := rows.Scan(
			&p.ID, &p.PersonID, &p.BookedOn, &p.PaidOn, &p.Amount, &p.Method, &note, &p.Slider,
			&p.Kind, &refID, &p.Period, &reversed, &reversedAt, &reversedBy, &createdAt, &createdBy,
			&p.PropertyID, &hasReleasedLinks,
		); err != nil {
			return history, ids, fmt.Errorf("scan payment history: %w", err)
		}
		original, inIncome := income[p.ID]
		shouldBeIncluded := !reversed && (p.PaidOn.Year() == rep.Year || taxRuleYear(p.PaidOn, p.Kind, p.Period) == rep.Year)
		if inIncome != shouldBeIncluded {
			return history, ids, errTaxExportChanged
		}
		if inIncome {
			changedValue := csvMoney(original.Amount) != csvMoney(p.Amount) || original.PropertyID != p.PropertyID
			changedDate := !sameTaxDay(original.PaidOn, p.PaidOn) || !sameTaxDay(original.BookedOn, p.BookedOn)
			if changedValue || changedDate {
				return history, ids, errTaxExportChanged
			}
			matched++
		}
		evidence := "nicht_zurueckgenommen"
		if reversed {
			history.Reversed++
			evidence = "zuordnungsnachweis_vorhanden"
			if !hasReleasedLinks {
				evidence = "zuordnungsnachweis_fehlt"
				history.Unverified++
			}
		}
		if err := writer.Write([]string{
			strconv.FormatInt(p.ID, 10), strconv.FormatInt(p.PersonID, 10), csvDate(p.BookedOn), csvDate(p.PaidOn),
			csvMoney(p.Amount), csvSafe(p.Method), csvSafe(note), taxHistoryBool(p.Slider), csvSafe(p.Kind),
			strconv.FormatInt(refID, 10), csvSafe(p.Period), taxHistoryBool(reversed), taxHistoryTime(reversedAt),
			taxHistoryID(reversedBy), createdAt.Format(time.RFC3339Nano), taxHistoryID(createdBy),
			strconv.FormatInt(p.PropertyID, 10), taxHistoryBool(inIncome), evidence,
		}); err != nil {
			return history, ids, fmt.Errorf("write payment history: %w", err)
		}
		ids = append(ids, p.ID)
		history.Count++
	}
	if err := rows.Err(); err != nil {
		return history, ids, fmt.Errorf("iterate payment history: %w", err)
	}
	if matched != len(income) {
		return history, ids, errTaxExportChanged
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return history, ids, fmt.Errorf("flush payment history: %w", err)
	}
	return history, ids, nil
}

func writeTaxPaymentAuditCSV(ctx context.Context, q dbQuerier, dst *bytes.Buffer, ids []int64) error {
	rows, err := q.Query(ctx, `SELECT a.id,a.created_at,a.user_id,a.username,a.action,a.entity,a.entity_id,
		a.summary,COALESCE(a.changes,'{}'::jsonb)::text FROM audit_log a
		WHERE a.entity IN ('payment','payment_tax_metadata') AND a.entity_id=ANY($1)
			OR a.entity='invoice' AND EXISTS(SELECT 1 FROM jsonb_array_elements(
				CASE WHEN jsonb_typeof(a.changes->'released_invoice_payments')='array'
				THEN a.changes->'released_invoice_payments' ELSE '[]'::jsonb END) link
				WHERE link->>'payment_id'=ANY(SELECT id::text FROM unnest($1::bigint[]) id))
		ORDER BY a.created_at,a.id`, ids)
	if err != nil {
		return fmt.Errorf("query payment audit history: %w", err)
	}
	defer rows.Close()
	dst.WriteString("\ufeff")
	writer := csv.NewWriter(dst)
	writer.Comma = ';'
	if err := writer.Write([]string{"audit_id", "zeitpunkt", "benutzer_id", "benutzer", "aktion", "entitaet", "entitaet_id", "beschreibung", "aenderungen_json"}); err != nil {
		return fmt.Errorf("write payment audit header: %w", err)
	}
	for rows.Next() {
		var id, entityID int64
		var userID *int64
		var at time.Time
		var username, action, entity, summary, changes string
		if err := rows.Scan(&id, &at, &userID, &username, &action, &entity, &entityID, &summary, &changes); err != nil {
			return fmt.Errorf("scan payment audit history: %w", err)
		}
		if err := writer.Write([]string{
			strconv.FormatInt(id, 10), at.Format(time.RFC3339Nano), taxHistoryID(userID), csvSafe(username),
			csvSafe(action), csvSafe(entity), strconv.FormatInt(entityID, 10), csvSafe(summary), csvSafe(changes),
		}); err != nil {
			return fmt.Errorf("write payment audit history: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate payment audit history: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush payment audit history: %w", err)
	}
	return nil
}

func taxHistoryBool(value bool) string {
	if value {
		return "ja"
	}
	return "nein"
}

func taxHistoryTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func taxHistoryID(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
