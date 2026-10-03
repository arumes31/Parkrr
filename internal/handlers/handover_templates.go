package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxHandoverTemplateContent = 4000

type handoverTemplate struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	AppliesTo string    `json:"applies_to"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type handoverTemplateRequest struct {
	Name      string `json:"name"`
	AppliesTo string `json:"applies_to"`
	Content   string `json:"content"`
}

func normalizeHandoverTemplate(in handoverTemplateRequest) (handoverTemplateRequest, string) {
	in.Name = trim(in.Name)
	in.AppliesTo = trim(in.AppliesTo)
	in.Content = trim(in.Content)
	if in.Name == "" {
		return in, "name is required"
	}
	if !validNameLength(in.Name) {
		return in, "name is too long"
	}
	if in.AppliesTo != "einlagerung" && in.AppliesTo != "auslagerung" && in.AppliesTo != "beide" {
		return in, "applies_to muss 'einlagerung', 'auslagerung' oder 'beide' sein"
	}
	if in.Content == "" {
		return in, "content is required"
	}
	if len(in.Content) > maxHandoverTemplateContent {
		return in, "content is too long"
	}
	return in, ""
}

// ListHandoverTemplates returns the shared handover templates in a stable order.
func (h *Handler) ListHandoverTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Pool.Query(r.Context(),
		`SELECT id, name, applies_to, content, created_at, updated_at
		   FROM handover_templates ORDER BY lower(name), id`)
	if err != nil {
		serverError(w, r, "could not load handover templates", err)
		return
	}
	defer rows.Close()
	out := []handoverTemplate{}
	for rows.Next() {
		var template handoverTemplate
		if err := rows.Scan(&template.ID, &template.Name, &template.AppliesTo, &template.Content,
			&template.CreatedAt, &template.UpdatedAt); err != nil {
			serverError(w, r, "could not load handover templates", err)
			return
		}
		out = append(out, template)
	}
	if err := rows.Err(); err != nil {
		serverError(w, r, "could not load handover templates", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateHandoverTemplate stores a reusable notes/checklist template.
func (h *Handler) CreateHandoverTemplate(w http.ResponseWriter, r *http.Request) {
	var request handoverTemplateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request, validationError := normalizeHandoverTemplate(request)
	if validationError != "" {
		writeError(w, http.StatusBadRequest, validationError)
		return
	}
	var template handoverTemplate
	err := h.Pool.QueryRow(r.Context(),
		`INSERT INTO handover_templates (name, applies_to, content)
		 VALUES ($1,$2,$3)
		 RETURNING id, name, applies_to, content, created_at, updated_at`,
		request.Name, request.AppliesTo, request.Content,
	).Scan(&template.ID, &template.Name, &template.AppliesTo, &template.Content,
		&template.CreatedAt, &template.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "template name already exists")
			return
		}
		serverError(w, r, "could not save handover template", err)
		return
	}
	h.auditCreated(r, "handover_template", template.ID, "Übergabevorlage angelegt: "+template.Name,
		map[string]any{"name": template.Name, "applies_to": template.AppliesTo, "content": template.Content})
	writeJSON(w, http.StatusCreated, template)
}

// UpdateHandoverTemplate changes one shared handover template.
func (h *Handler) UpdateHandoverTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var request handoverTemplateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	request, validationError := normalizeHandoverTemplate(request)
	if validationError != "" {
		writeError(w, http.StatusBadRequest, validationError)
		return
	}

	var updated handoverTemplate
	err := pgx.BeginFunc(r.Context(), h.Pool, func(tx pgx.Tx) error {
		var old handoverTemplate
		if err := tx.QueryRow(r.Context(),
			`SELECT id, name, applies_to, content, created_at, updated_at
			   FROM handover_templates WHERE id=$1 FOR UPDATE`, id,
		).Scan(&old.ID, &old.Name, &old.AppliesTo, &old.Content,
			&old.CreatedAt, &old.UpdatedAt); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(),
			`UPDATE handover_templates SET name=$2, applies_to=$3, content=$4, updated_at=now()
			  WHERE id=$1
			  RETURNING id, name, applies_to, content, created_at, updated_at`,
			id, request.Name, request.AppliesTo, request.Content,
		).Scan(&updated.ID, &updated.Name, &updated.AppliesTo, &updated.Content,
			&updated.CreatedAt, &updated.UpdatedAt); err != nil {
			return err
		}
		return h.auditChangeTx(r.Context(), tx, r, "update", "handover_template", id,
			"Übergabevorlage geändert: "+updated.Name, diffFields(old, updated, "ID", "CreatedAt", "UpdatedAt"))
	})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "template not found")
		case isUniqueViolation(err):
			writeError(w, http.StatusConflict, "template name already exists")
		default:
			serverError(w, r, "could not update handover template", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteHandoverTemplate removes a shared handover template.
func (h *Handler) DeleteHandoverTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var name, appliesTo, content string
	err := h.Pool.QueryRow(r.Context(),
		`DELETE FROM handover_templates WHERE id=$1 RETURNING name, applies_to, content`, id,
	).Scan(&name, &appliesTo, &content)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "template not found")
			return
		}
		serverError(w, r, "could not delete handover template", err)
		return
	}
	h.auditDeleted(r, "handover_template", id,
		"Übergabevorlage gelöscht: "+name+" ("+strconv.FormatInt(id, 10)+")",
		map[string]any{"name": name, "applies_to": appliesTo, "content": content})
	w.WriteHeader(http.StatusNoContent)
}
