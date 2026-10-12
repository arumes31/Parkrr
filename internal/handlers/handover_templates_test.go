package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func handoverTemplateRequestBody(t *testing.T, name, appliesTo, content string) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(handoverTemplateRequest{Name: name, AppliesTo: appliesTo, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func TestHandoverTemplateValidation(t *testing.T) {
	tests := []struct {
		name string
		req  handoverTemplateRequest
		want string
	}{
		{"valid", handoverTemplateRequest{"Standard", "beide", "☐ Fahrzeug prüfen"}, ""},
		{"name", handoverTemplateRequest{"", "beide", "Text"}, "name is required"},
		{"direction", handoverTemplateRequest{"X", "sonderfall", "Text"}, "applies_to"},
		{"content", handoverTemplateRequest{"X", "beide", ""}, "content is required"},
		{"long content", handoverTemplateRequest{"X", "beide", strings.Repeat("x", maxHandoverTemplateContent+1)}, "content is too long"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, got := normalizeHandoverTemplate(test.req)
			if !strings.Contains(got, test.want) {
				t.Fatalf("validation = %q, want containing %q", got, test.want)
			}
		})
	}
}

func TestHandoverTemplateLifecycle(t *testing.T) {
	h := testHandler(t)
	name := "Integration Übergabe " + strconv.FormatInt(h.now().UnixNano(), 10)
	create := httptest.NewRecorder()
	h.CreateHandoverTemplate(create, httptest.NewRequest(http.MethodPost, "/api/handover-templates",
		handoverTemplateRequestBody(t, name, "einlagerung", "☐ Zustand prüfen")))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var template handoverTemplate
	if err := json.Unmarshal(create.Body.Bytes(), &template); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// t.Context() is already canceled when cleanup callbacks run.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := h.Pool.Exec(ctx, `DELETE FROM handover_templates WHERE id=$1`, template.ID); err != nil {
			t.Errorf("cleanup handover template: %v", err)
		}
	})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/handover-templates/"+strconv.FormatInt(template.ID, 10),
		handoverTemplateRequestBody(t, name+" geändert", "beide", "☐ Zustand\n☐ Schlüssel"))
	updateReq.SetPathValue("id", strconv.FormatInt(template.ID, 10))
	update := httptest.NewRecorder()
	h.UpdateHandoverTemplate(update, updateReq)
	if update.Code != http.StatusOK {
		t.Fatalf("update: %d %s", update.Code, update.Body.String())
	}

	list := httptest.NewRecorder()
	h.ListHandoverTemplates(list, httptest.NewRequest(http.MethodGet, "/api/handover-templates", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Schlüssel") {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/handover-templates/"+strconv.FormatInt(template.ID, 10), nil)
	deleteReq.SetPathValue("id", strconv.FormatInt(template.ID, 10))
	deleted := httptest.NewRecorder()
	h.DeleteHandoverTemplate(deleted, deleteReq)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
}
