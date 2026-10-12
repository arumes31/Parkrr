package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessOverall(t *testing.T) {
	tests := []struct {
		name  string
		items []readinessItem
		want  string
	}{
		{"ready", []readinessItem{{State: "ready"}}, "ready"},
		{"warning", []readinessItem{{State: "ready"}, {State: "warning"}}, "warning"},
		{"blocked wins", []readinessItem{{State: "warning"}, {State: "blocked"}}, "blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := readinessOverall(test.items); got != test.want {
				t.Fatalf("readinessOverall() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestOperationalReadinessResponse(t *testing.T) {
	h := testHandler(t)
	recorder := httptest.NewRecorder()
	h.OperationalReadiness(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/readiness", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response readinessResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 6 {
		t.Fatalf("items = %d, want 6", len(response.Items))
	}
	if response.State != "blocked" {
		t.Fatalf("state = %q, want blocked for an unconfigured handler", response.State)
	}
}
