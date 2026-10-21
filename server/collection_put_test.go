package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// A collection's config arrives in halves now. A browser tab used to send its
// whole copy on every view change, and that copy put back a schema from
// before an agent's new property.
func TestACollectionConfigHalfLeavesTheOtherAlone(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "owner@example.test")
	ws := s.firstWorkspaceOf(t, uid)
	col := s.makeCollection(t, ws, uid, "Tasks", `[{"id":"title2","name":"Notes","type":"text"}]`)
	put := func(body string) int {
		return requestAs(t, s, cookie, "PUT", "/api/collections/"+col, body).Code
	}
	stored := func() (schema, views string) {
		s.db.QueryRow(`SELECT schema, views FROM collections WHERE page_id = ?`, col).Scan(&schema, &views)
		return
	}

	// An agent adds a property.
	if _, err := callTool(t, s, &user{ID: uid}, "update_schema", `{"page_id":"`+col+`","properties":[{"name":"Priority","type":"text"}]}`); err != nil {
		t.Fatalf("update_schema: %v", err)
	}
	// A tab that loaded the collection before renames a view: views only.
	if code := put(`{"views":[{"id":"t","name":"Everything","type":"table"}]}`); code != 200 {
		t.Fatalf("views alone: %d", code)
	}
	schema, views := stored()
	if !strings.Contains(schema, "Priority") {
		t.Errorf("a view change took the agent's property away: %s", schema)
	}
	if !strings.Contains(views, "Everything") {
		t.Errorf("the view change was not stored: %s", views)
	}
	// And the schema alone leaves the views.
	if code := put(`{"schema":[{"id":"title2","name":"Notes","type":"text"}]}`); code != 200 {
		t.Fatalf("schema alone: %d", code)
	}
	if _, views := stored(); !strings.Contains(views, "Everything") {
		t.Errorf("a schema change replaced the views: %s", views)
	}
	// Both together still work, and nothing at all is refused.
	if code := put(`{"schema":[],"views":[{"id":"t","name":"T","type":"table"}]}`); code != 200 {
		t.Errorf("both halves: %d", code)
	}
	if code := put(`{}`); code != 400 {
		t.Errorf("an empty body: %d, want 400", code)
	}
	// The answer is what is stored, both halves.
	rec := requestAs(t, s, cookie, "PUT", "/api/collections/"+col, `{"views":[{"id":"t","name":"Again","type":"table"}]}`)
	var got map[string]json.RawMessage
	if json.Unmarshal(rec.Body.Bytes(), &got) != nil || string(got["schema"]) != "[]" || !strings.Contains(string(got["views"]), "Again") {
		t.Errorf("answer: %s", rec.Body.String())
	}
}
