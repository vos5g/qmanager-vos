package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The built-in scenarios live in the UI; the list endpoint must only return
// the user's custom ones, or each built-in shows up twice.
func TestScenariosListHasNoBuiltins(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.HandleScenariosList(rec, httptest.NewRequest(http.MethodGet, "/cgi-bin/quecmanager/scenarios/list.sh", nil))
	var out struct {
		Success   bool             `json:"success"`
		Scenarios []map[string]any `json:"scenarios"`
		Active    string           `json:"active_scenario_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Success || out.Scenarios == nil || out.Active == "" {
		t.Fatalf("bad reply %q: %v", rec.Body.String(), err)
	}
	for _, sc := range out.Scenarios {
		if id, _ := sc["id"].(string); isBuiltinScenario(id) {
			t.Fatalf("built-in %q returned as a custom scenario", id)
		}
	}
}
