package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

const legacyBindingJSON = `{
  "format": 6,
  "name": "webshop",
  "cwd": "/repo/webshop",
  "builder": {"kind": "opencode", "pane_id": "w2:p4"},
  "builder_candidate": "opencode/openai/gpt-4o",
  "role": "designer",
  "builder_missing_since": "2026-09-20T09:00:00Z",
  "builder_screen": "the old screen",
  "builder_screen_at": "2026-09-20T09:01:00Z",
  "consults": [{"id": "7f2a3c1d", "role": "reviewer"}],
  "state": "active"
}`

const newBindingJSON = `{
  "format": 6,
  "name": "webshop",
  "cwd": "/repo/webshop",
  "runner": {"kind": "opencode", "pane_id": "w2:p4"},
  "candidate": "opencode/openai/gpt-4o",
  "actor": "designer",
  "runner_missing_since": "2026-09-20T09:00:00Z",
  "runner_screen": "the old screen",
  "runner_screen_at": "2026-09-20T09:01:00Z",
  "consults": [{"id": "7f2a3c1d", "actor": "reviewer"}],
  "state": "active"
}`

// TestBindingDecodesFormat6Record pins the lazy decode: a format-6 record with
// the pre-A4 keys yields the same Go values as one with the new keys, and
// re-encoding it writes only the new keys, at the current format.
func TestBindingDecodesFormat6Record(t *testing.T) {
	got, want := decodeLegacyAndNewBinding(t)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy decode = %+v, want the new-key decode %+v", got, want)
	}
	assertBindingReEncodesNewKeys(t, got)

	gotConsult := decodeJSON[Consult](t, `{"id": "7f2a3c1d", "role": "reviewer"}`)
	wantConsult := decodeJSON[Consult](t, `{"id": "7f2a3c1d", "actor": "reviewer"}`)
	if !reflect.DeepEqual(gotConsult, wantConsult) {
		t.Errorf("legacy consult = %+v, want %+v", gotConsult, wantConsult)
	}
	assertEncodesNewKey(t, gotConsult, `"actor":"reviewer"`, `"role"`)

	gotLog := decodeJSON[LogEntry](t, `{"ts":"2026-09-20T09:00:00Z","round":1,"direction":"to_builder","kind":"plan","builder_session":{"kind":"claude","id":"s1"}}`)
	wantLog := decodeJSON[LogEntry](t, `{"ts":"2026-09-20T09:00:00Z","round":1,"direction":"to_runner","kind":"plan","runner_session":{"kind":"claude","id":"s1"}}`)
	if !reflect.DeepEqual(gotLog, wantLog) {
		t.Errorf("legacy log entry = %+v, want %+v", gotLog, wantLog)
	}
	assertEncodesNewKey(t, gotLog, `"direction":"to_runner"`, "to_builder")
	assertEncodesNewKey(t, gotLog, `"runner_session"`, "builder_session")
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	return v
}

func decodeLegacyAndNewBinding(t *testing.T) (Binding, Binding) {
	t.Helper()
	return decodeJSON[Binding](t, legacyBindingJSON), decodeJSON[Binding](t, newBindingJSON)
}

// assertBindingReEncodesNewKeys saves the decoded record and pins that the
// stored JSON carries the A4 keys and none of the old ones.
func assertBindingReEncodesNewKeys(t *testing.T, got Binding) {
	t.Helper()
	s := New(t.TempDir())
	if err := s.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := bindingRecordJSON(t, s, "webshop")
	for _, wantKey := range []string{fmt.Sprintf(`"format":%d`, BindingFormat), `"shape":"writer"`, `"runner":`, `"candidate":"opencode/openai/gpt-4o"`, `"actor":"designer"`, `"runner_missing_since":`, `"runner_screen":`, `"runner_screen_at":`, `"actor":"reviewer"`} {
		if !bytes.Contains(raw, []byte(wantKey)) {
			t.Errorf("re-encoded record lacks %s:\n%s", wantKey, raw)
		}
	}
	for _, oldKey := range []string{`"builder"`, `"builder_candidate"`, `"builder_missing_since"`, `"builder_screen"`, `"role"`} {
		if bytes.Contains(raw, []byte(oldKey)) {
			t.Errorf("re-encoded record still carries %s:\n%s", oldKey, raw)
		}
	}
}

// assertEncodesNewKey pins that marshalling v carries want and not old.
func assertEncodesNewKey(t *testing.T, v any, want, old string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(want)) {
		t.Errorf("re-encoded = %s, want %s", encoded, want)
	}
	if bytes.Contains(encoded, []byte(old)) {
		t.Errorf("re-encoded = %s, want no %s", encoded, old)
	}
}

// TestBindingEmptyRoleReadsAsBuilder pins that a record with no role decodes to
// the builder actor and encodes it back out, rather than an empty actor.
func TestBindingEmptyRoleReadsAsBuilder(t *testing.T) {
	b := decodeJSON[Binding](t, `{"name":"webshop","cwd":"/repo","format":6}`)
	if b.Role != "builder" {
		t.Errorf("Role = %q, want builder", b.Role)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"actor":"builder"`)) {
		t.Errorf("encoded record = %s, want actor \"builder\"", raw)
	}
}
