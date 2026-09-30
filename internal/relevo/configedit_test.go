package relevo

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// configeditCandidatesJSON is this machine's seven candidates, with the names
// the store derives for them.
const configeditCandidatesJSON = `[
  {"name": "gemini-3.8-flash-high", "harness": "agy", "provider": "google", "model": "gemini-3.8-flash-high"},
  {"name": "claude-sonnet-4-6", "harness": "agy", "provider": "agy-extra", "model": "claude-sonnet-4-6"},
  {"name": "sonnet", "harness": "claude", "provider": "anthropic", "model": "sonnet"},
  {"name": "haiku", "harness": "claude", "provider": "anthropic", "model": "haiku"},
  {"name": "glm-5.3-flash", "harness": "opencode", "provider": "openrouter", "model": "z-ai/glm-5.3-flash"},
  {"name": "deepseek-v4.1-flash", "harness": "opencode", "provider": "cline-pass", "model": "cline-pass/deepseek-v4.1-flash#high"},
  {"name": "gpt-5.6-terra", "harness": "codex", "provider": "openai", "model": "gpt-5.6-terra:high"}
]`

// configeditDoc builds the fixture doc directly: the seven candidates above,
// the three actors, and no custom agents. Policy allows the yolo tiers the
// actors carry, as the machine's policy does.
func configeditDoc(t *testing.T) ConfigDoc {
	t.Helper()
	var doc ConfigDoc
	if err := json.Unmarshal([]byte(configeditCandidatesJSON), &doc.Candidates); err != nil {
		t.Fatalf("candidates JSON: %v", err)
	}
	if len(doc.Candidates) != 7 {
		t.Fatalf("fixture has %d candidates, want 7", len(doc.Candidates))
	}
	doc.Policy = policy.Policy{MaxTier: "yolo"}
	doc.Actors = map[string]roles.Actor{
		"builder": {
			Agent: "plan-executor",
			Candidates: []roles.Entry{
				{Candidate: "gemini-3.8-flash-high"},
				{Candidate: "claude-sonnet-4-6"},
				{Candidate: "deepseek-v4.1-flash"},
				{Candidate: "gpt-5.6-terra"},
				{Candidate: "sonnet"},
				{Candidate: "glm-5.3-flash"},
			},
			Tier: "yolo",
		},
		"reviewer": {
			Agent: "reviewer",
			Candidates: []roles.Entry{
				{Candidate: "sonnet"},
				{Candidate: "gpt-5.6-terra"},
			},
			Tier: "yolo",
		},
		"researcher": {
			Agent:      "researcher",
			Candidates: []roles.Entry{{Candidate: "haiku"}},
		},
	}
	doc.Agents = map[string]roles.AgentEntry{}
	return doc
}

func wantFieldError(t *testing.T, err error, field, msg string) {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v (%T), want *FieldError", err, err)
	}
	if fe.Field != field || fe.Msg != msg {
		t.Errorf("FieldError = {%q, %q}, want {%q, %q}", fe.Field, fe.Msg, field, msg)
	}
}

func decodeCandidates(t *testing.T, e ConfigEdit) []candidate.Candidate {
	t.Helper()
	raw, ok := e.Sections[config.Candidates]
	if !ok {
		t.Fatal("edit changes no candidates section")
	}
	var out []candidate.Candidate
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	return out
}

func decodeActors(t *testing.T, e ConfigEdit) map[string]roles.Actor {
	t.Helper()
	raw, ok := e.Sections[config.Actors]
	if !ok {
		t.Fatal("edit changes no actors section")
	}
	var out map[string]roles.Actor
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	return out
}

func decodeAgents(t *testing.T, e ConfigEdit) map[string]roles.AgentEntry {
	t.Helper()
	raw, ok := e.Sections[config.Agents]
	if !ok {
		t.Fatal("edit changes no agents section")
	}
	var out map[string]roles.AgentEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	return out
}

func findCandidate(cands []candidate.Candidate, name string) (candidate.Candidate, bool) {
	for _, c := range cands {
		if c.Name == name {
			return c, true
		}
	}
	return candidate.Candidate{}, false
}

func TestConfigEditCandidateValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		in    CandidateInput
		field string
		msg   string
	}{
		{"unknown harness", CandidateInput{Harness: "nope", Provider: "p", Model: "m"}, "harness", "pick a harness"},
		{"empty provider", CandidateInput{Harness: "codex", Provider: "", Model: "m"}, "provider", "a provider is required"},
		{"provider with a slash", CandidateInput{Harness: "codex", Provider: "a/b", Model: "m"}, "provider", "a provider is one word, with no /"},
		{"provider outside the kind's list", CandidateInput{Harness: "agy", Provider: "anthropic", Model: "m"}, "provider", "pick one of agy's providers"},
		{"empty model", CandidateInput{Harness: "codex", Provider: "openai", Model: "   "}, "model", "a model is required"},
		{"duplicate triple", CandidateInput{Harness: "claude", Provider: "anthropic", Model: "haiku"}, "model", "already a candidate: haiku"},
		{"provider is a candidate name", CandidateInput{Harness: "codex", Provider: "sonnet", Model: "brand-new"}, "provider", "sonnet is already a candidate's name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AddCandidate(configeditDoc(t), tc.in)
			wantFieldError(t, err, tc.field, tc.msg)
		})
	}
}

func TestConfigEditAddCandidate(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	edit, err := AddCandidate(doc, CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if len(edit.Sections) != 1 {
		t.Fatalf("sections = %v, want only candidates", edit.Sections)
	}
	if edit.Name != "deepseek-v4.2-flash" || edit.Message != "add candidate deepseek-v4.2-flash" {
		t.Errorf("edit = %+v, want name/message for deepseek-v4.2-flash", edit)
	}
	got := decodeCandidates(t, edit)
	if len(got) != 8 {
		t.Fatalf("candidates = %d, want 8", len(got))
	}
	last := got[len(got)-1]
	if last.Name != "deepseek-v4.2-flash" || last.Harness != "opencode" ||
		last.Provider != "cline-pass" || last.Model != "cline-pass/deepseek-v4.2-flash#high" {
		t.Errorf("added entry = %+v", last)
	}
	if len(doc.Candidates) != 7 {
		t.Errorf("doc was mutated: %d candidates", len(doc.Candidates))
	}
}

func TestConfigEditEditCandidateRenames(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	edit, err := EditCandidate(doc, "deepseek-v4.1-flash", CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if err != nil {
		t.Fatalf("EditCandidate: %v", err)
	}
	if edit.Name != "deepseek-v4.2-flash" {
		t.Errorf("Name = %q, want deepseek-v4.2-flash", edit.Name)
	}
	if edit.Message != "edit candidate deepseek-v4.1-flash → deepseek-v4.2-flash" {
		t.Errorf("Message = %q", edit.Message)
	}

	cands := decodeCandidates(t, edit)
	if _, stale := findCandidate(cands, "deepseek-v4.1-flash"); stale {
		t.Error("old name is still a candidate")
	}
	c, ok := findCandidate(cands, "deepseek-v4.2-flash")
	if !ok || c.Model != "cline-pass/deepseek-v4.2-flash#high" {
		t.Fatalf("renamed entry = %+v (found %v)", c, ok)
	}

	acts := decodeActors(t, edit)
	if got := acts["builder"].Candidates[2].Candidate; got != "deepseek-v4.2-flash" {
		t.Errorf("builder entry 3 = %q, want deepseek-v4.2-flash", got)
	}
}

func TestConfigEditEditCandidateKeepsFields(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	doc.Candidates[2].Tier = "edit"
	doc.Candidates[2].LimitPatterns = []string{`(?i)mine`}

	edit, err := EditCandidate(doc, "sonnet", CandidateInput{Harness: "claude", Provider: "anthropic", Model: "sonnet-4"})
	if err != nil {
		t.Fatalf("EditCandidate: %v", err)
	}
	c, ok := findCandidate(decodeCandidates(t, edit), "sonnet-4")
	if !ok {
		t.Fatal("edited entry is missing")
	}
	if c.Tier != "edit" {
		t.Errorf("Tier = %q, want edit", c.Tier)
	}
	if !reflect.DeepEqual(c.LimitPatterns, []string{`(?i)mine`}) {
		t.Errorf("LimitPatterns = %v", c.LimitPatterns)
	}
}

func TestConfigEditEditCandidateUnchanged(t *testing.T) {
	t.Parallel()

	_, err := EditCandidate(configeditDoc(t), "sonnet", CandidateInput{Harness: "claude", Provider: "anthropic", Model: "sonnet"})
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("err = %v, want ErrNoChange", err)
	}
}

// TestPreviewCandidateName pins the preview the candidate form shows: the
// name an edit would take, and "" for an add with no model yet.
func TestPreviewCandidateName(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)

	got := PreviewCandidateName(doc, "deepseek-v4.1-flash", CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if got != "deepseek-v4.2-flash" {
		t.Errorf("edit preview = %q, want deepseek-v4.2-flash", got)
	}

	if got := PreviewCandidateName(doc, "", CandidateInput{Harness: "opencode", Provider: "openrouter"}); got != "" {
		t.Errorf("add preview with no model = %q, want empty", got)
	}

	// The preview agrees with the edit each call derives.
	edit, err := EditCandidate(doc, "deepseek-v4.1-flash", CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if err != nil {
		t.Fatalf("EditCandidate: %v", err)
	}
	if edit.Name != got {
		t.Errorf("EditCandidate name = %q, preview = %q", edit.Name, got)
	}
}

func TestConfigEditEditCandidateOffEntryKeepsOff(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	b := doc.Actors["builder"]
	b.Candidates[2].Off = true
	doc.Actors["builder"] = b

	edit, err := EditCandidate(doc, "deepseek-v4.1-flash", CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if err != nil {
		t.Fatalf("EditCandidate: %v", err)
	}
	found := false
	for _, e := range decodeActors(t, edit)["builder"].Candidates {
		if e.Candidate == "deepseek-v4.2-flash" {
			found = true
			if !e.Off {
				t.Error("Off was dropped through the rename")
			}
		}
	}
	if !found {
		t.Error("builder has no entry for the renamed candidate")
	}
}

func TestConfigEditDeleteCandidateRefusesLast(t *testing.T) {
	t.Parallel()

	_, err := DeleteCandidate(configeditDoc(t), "haiku")
	wantFieldError(t, err, "", "researcher has no other candidate; add one in :actors first")
}

func TestConfigEditDeleteCandidateRemovesFromActors(t *testing.T) {
	t.Parallel()

	edit, err := DeleteCandidate(configeditDoc(t), "gpt-5.6-terra")
	if err != nil {
		t.Fatalf("DeleteCandidate: %v", err)
	}
	if edit.Message != "delete candidate gpt-5.6-terra" {
		t.Errorf("Message = %q", edit.Message)
	}
	if _, ok := findCandidate(decodeCandidates(t, edit), "gpt-5.6-terra"); ok {
		t.Error("gpt-5.6-terra is still a candidate")
	}
	acts := decodeActors(t, edit)
	for _, name := range []string{"builder", "reviewer"} {
		for _, e := range acts[name].Candidates {
			if e.Candidate == "gpt-5.6-terra" {
				t.Errorf("%s still lists gpt-5.6-terra", name)
			}
		}
	}
	if len(acts["reviewer"].Candidates) != 1 {
		t.Errorf("reviewer has %d candidates, want 1", len(acts["reviewer"].Candidates))
	}
}

func TestCandidateSlots(t *testing.T) {
	t.Parallel()

	got := CandidateSlots(configeditDoc(t), "gpt-5.6-terra")
	want := []ActorSlot{{Actor: "builder", Position: 4}, {Actor: "reviewer", Position: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("slots = %+v, want %+v", got, want)
	}
}

func TestConfigEditSetActorEntries(t *testing.T) {
	t.Parallel()

	t.Run("rejects an unknown name", func(t *testing.T) {
		_, err := SetActorEntries(configeditDoc(t), "builder", []roles.Entry{{Candidate: "nope"}})
		wantFieldError(t, err, "", "no candidate named nope")
	})

	t.Run("rejects a duplicate", func(t *testing.T) {
		_, err := SetActorEntries(configeditDoc(t), "builder", []roles.Entry{
			{Candidate: "sonnet"},
			{Candidate: "sonnet"},
		})
		wantFieldError(t, err, "", "duplicate candidate sonnet")
	})

	t.Run("replaces the list", func(t *testing.T) {
		edit, err := SetActorEntries(configeditDoc(t), "builder", []roles.Entry{
			{Candidate: "gemini-3.8-flash-high"},
			{Candidate: "sonnet", Off: true},
		})
		if err != nil {
			t.Fatalf("SetActorEntries: %v", err)
		}
		if edit.Message != "edit actor builder candidates" {
			t.Errorf("Message = %q", edit.Message)
		}
		got := decodeActors(t, edit)["builder"].Candidates
		want := []roles.Entry{{Candidate: "gemini-3.8-flash-high"}, {Candidate: "sonnet", Off: true}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entries = %+v, want %+v", got, want)
		}
	})
}

func TestConfigEditEditActor(t *testing.T) {
	t.Parallel()

	t.Run("a builtin actor keeps its shape", func(t *testing.T) {
		_, err := EditActor(configeditDoc(t), "builder", "reviewer", "yolo", true)
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v (%T), want *FieldError", err, err)
		}
		if fe.Field != "" || !strings.Contains(fe.Msg, "must run a writer agent") {
			t.Errorf("FieldError = {%q, %q}", fe.Field, fe.Msg)
		}
	})

	t.Run("a reader agent stores no check", func(t *testing.T) {
		edit, err := EditActor(configeditDoc(t), "reviewer", "reviewer", "yolo", true)
		if err != nil {
			t.Fatalf("EditActor: %v", err)
		}
		if got := decodeActors(t, edit)["reviewer"].Check; got != nil {
			t.Errorf("Check = %v, want nil on a reader", *got)
		}
	})
}

// TestConfigEditPreservesPlacement pins that the two edit APIs that mutate a
// loaded actor copy leave an existing placement untouched.
func TestConfigEditPreservesPlacement(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	builder := doc.Actors["builder"]
	builder.Placement = []string{"zen", "local"}
	doc.Actors["builder"] = builder

	entries, err := SetActorEntries(doc, "builder", []roles.Entry{{Candidate: "sonnet"}})
	if err != nil {
		t.Fatalf("SetActorEntries: %v", err)
	}
	if got := decodeActors(t, entries)["builder"].Placement; !reflect.DeepEqual(got, []string{"zen", "local"}) {
		t.Errorf("SetActorEntries placement = %v, want [zen local]", got)
	}

	edited, err := EditActor(doc, "builder", "plan-executor", "yolo", true)
	if err != nil {
		t.Fatalf("EditActor: %v", err)
	}
	if got := decodeActors(t, edited)["builder"].Placement; !reflect.DeepEqual(got, []string{"zen", "local"}) {
		t.Errorf("EditActor placement = %v, want [zen local]", got)
	}
}

func TestConfigEditAddAndDeleteActor(t *testing.T) {
	t.Parallel()

	doc := configeditDoc(t)
	add, err := AddActor(doc, "custom", "reviewer")
	if err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	if add.Message != "add actor custom" || add.Name != "custom" {
		t.Errorf("add = %+v", add)
	}
	got := decodeActors(t, add)["custom"]
	if got.Agent != "reviewer" || len(got.Candidates) != 0 || got.Tier != "" || got.Check != nil {
		t.Errorf("new actor = %+v, want an empty reader", got)
	}

	next := configeditDoc(t)
	next.Actors = decodeActors(t, add)
	del, err := DeleteActor(next, "custom")
	if err != nil {
		t.Fatalf("DeleteActor: %v", err)
	}
	if del.Message != "delete actor custom" {
		t.Errorf("delete = %+v", del)
	}
	if _, ok := decodeActors(t, del)["custom"]; ok {
		t.Error("custom is still an actor")
	}

	_, err = DeleteActor(configeditDoc(t), "builder")
	wantFieldError(t, err, "", "builder is built in; it can't be deleted")
}

func TestConfigEditDeleteAgent(t *testing.T) {
	t.Parallel()

	_, err := DeleteAgent(configeditDoc(t), "reviewer")
	wantFieldError(t, err, "", "reviewer ships with relevo; it can't be deleted")

	used := configeditDoc(t)
	used.Agents["scout"] = roles.AgentEntry{
		Shape:  "reader",
		Native: map[string]roles.DefRow{"opencode": {Agent: "scout"}},
	}
	used.Actors["tinkerer"] = roles.Actor{Agent: "scout"}
	_, err = DeleteAgent(used, "scout")
	wantFieldError(t, err, "", "used by tinkerer; point it at another agent in :actors first")

	unused := configeditDoc(t)
	unused.Agents["odd"] = roles.AgentEntry{
		Shape:  "reader",
		Native: map[string]roles.DefRow{"opencode": {Agent: "odd"}},
	}
	edit, err := DeleteAgent(unused, "odd")
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if edit.Message != "delete agent odd" {
		t.Errorf("Message = %q", edit.Message)
	}
	if len(edit.Sections) != 1 {
		t.Fatalf("sections = %v, want only agents", edit.Sections)
	}
	if _, ok := decodeAgents(t, edit)["odd"]; ok {
		t.Error("odd is still an agent")
	}
}

func TestConfigEditWriteAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)

	edit, err := AddCandidate(configeditDoc(t), CandidateInput{
		Harness: "opencode", Provider: "cline-pass", Model: "cline-pass/deepseek-v4.2-flash#high",
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if err := WriteConfigEdit(st, edit); err != nil {
		t.Fatalf("WriteConfigEdit: %v", err)
	}

	rows, err := st.Log(1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("revisions = %d, want 1", len(rows))
	}
	if rows[0].Source != "ui" || rows[0].Message != edit.Message {
		t.Errorf("revision = {source: %q, message: %q}, want {ui, %q}", rows[0].Source, rows[0].Message, edit.Message)
	}

	got, err := LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	if len(got.Candidates) != 8 {
		t.Fatalf("loaded %d candidates, want 8", len(got.Candidates))
	}
	c, ok := findCandidate(got.Candidates, "deepseek-v4.2-flash")
	if !ok || c.Model != "cline-pass/deepseek-v4.2-flash#high" {
		t.Errorf("loaded entry = %+v (found %v)", c, ok)
	}
}

func TestReloadConfig(t *testing.T) {
	t.Parallel()

	t.Run("no store is an error", func(t *testing.T) {
		if _, err := ReloadConfig(Runtime{}); err == nil || err.Error() != "no config store" {
			t.Fatalf("err = %v, want no config store", err)
		}
	})

	t.Run("picks up an added candidate", func(t *testing.T) {
		d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		st := config.Open(d)

		edit, err := AddCandidate(configeditDoc(t), CandidateInput{Harness: "claude", Provider: "anthropic", Model: "opus-5"})
		if err != nil {
			t.Fatalf("AddCandidate: %v", err)
		}
		if err := WriteConfigEdit(st, edit); err != nil {
			t.Fatalf("WriteConfigEdit: %v", err)
		}

		out, err := ReloadConfig(Runtime{Config: st})
		if err != nil {
			t.Fatalf("ReloadConfig: %v", err)
		}
		if out.Candidates == nil || out.Candidates.Len() != 8 {
			t.Fatalf("Candidates = %v, want 8", out.Candidates)
		}
		if _, err := out.Candidates.Resolve("opus-5"); err != nil {
			t.Errorf("Resolve(opus-5): %v", err)
		}
	})
}

// decodeServers is one edit's servers section, decoded.
func decodeServers(t *testing.T, e ConfigEdit) map[string]remote.ServerEntry {
	t.Helper()
	raw, ok := e.Sections[config.Servers]
	if !ok {
		t.Fatal("edit changes no servers section")
	}
	var out map[string]remote.ServerEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode servers: %v", err)
	}
	return out
}

// configeditServerDoc is configeditDoc with a servers section and the builder
// placed on zen, so the delete guard has an actor to name.
func configeditServerDoc(t *testing.T) ConfigDoc {
	t.Helper()
	doc := configeditDoc(t)
	doc.Servers = map[string]remote.ServerEntry{
		"backup": {URL: "https://backup:7777", Fingerprint: "sha256:bbbb"},
		"zen":    {URL: "https://zen:7777", Fingerprint: "sha256:aaaa"},
	}
	b := doc.Actors["builder"]
	b.Placement = []string{"zen"}
	doc.Actors["builder"] = b
	return doc
}

// A server edit changes the servers section alone, and the store's own read
// path hands it back: add, edit and delete all round-trip.
func TestConfigEditServersRoundTrip(t *testing.T) {
	t.Parallel()

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)

	add, err := AddServer(configeditDoc(t), "zen", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:aaaa"})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if add.Message != "add server zen" || add.Name != "zen" {
		t.Errorf("add edit = {message: %q, name: %q}", add.Message, add.Name)
	}
	if err := WriteConfigEdit(st, add); err != nil {
		t.Fatalf("WriteConfigEdit(add): %v", err)
	}
	loaded, err := LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	if got := loaded.Servers["zen"]; got.URL != "https://zen:7777" || got.Fingerprint != "sha256:aaaa" {
		t.Fatalf("loaded zen = %+v", got)
	}

	edit, err := EditServer(loaded, "zen", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:cccc"})
	if err != nil {
		t.Fatalf("EditServer: %v", err)
	}
	if edit.Message != "edit server zen" {
		t.Errorf("edit message = %q", edit.Message)
	}
	if err := WriteConfigEdit(st, edit); err != nil {
		t.Fatalf("WriteConfigEdit(edit): %v", err)
	}
	loaded, err = LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc after the edit: %v", err)
	}
	if got := loaded.Servers["zen"].Fingerprint; got != "sha256:cccc" {
		t.Fatalf("zen fingerprint = %q after the edit", got)
	}

	del, err := DeleteServer(loaded, "zen")
	if err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if del.Message != "delete server zen" {
		t.Errorf("delete message = %q", del.Message)
	}
	if err := WriteConfigEdit(st, del); err != nil {
		t.Fatalf("WriteConfigEdit(delete): %v", err)
	}
	loaded, err = LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc after the delete: %v", err)
	}
	if _, ok := loaded.Servers["zen"]; ok {
		t.Error("zen is still stored after the delete")
	}
}

// AddServer refuses a name that cannot key the section and an entry the store
// would refuse, each as the FieldError the form shows.
func TestConfigEditAddServerRefusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		field string
		msg   string
		add   func(d ConfigDoc) (ConfigEdit, error)
	}{
		{
			name: "empty name", field: "name", msg: "a name is required",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:aaaa"})
			},
		},
		{
			name: "local is reserved", field: "name", msg: "local is reserved for placement",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "local", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:aaaa"})
			},
		},
		{
			name: "already a server", field: "name", msg: "server zen already exists",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "zen", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:aaaa"})
			},
		},
		{
			name: "no url", field: "url", msg: "new: invalid url",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "new", remote.ServerEntry{})
			},
		},
		{
			name: "https without a fingerprint or ca", field: "url", msg: "new: fingerprint or ca required for https",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "new", remote.ServerEntry{URL: "https://new:7777"})
			},
		},
		{
			name: "http without insecure", field: "url", msg: "new: https required unless insecure",
			add: func(d ConfigDoc) (ConfigEdit, error) {
				return AddServer(d, "new", remote.ServerEntry{URL: "http://new:7777"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.add(configeditServerDoc(t))
			wantFieldError(t, err, tc.field, tc.msg)
		})
	}
}

// DeleteServer refuses a server an actor's placement names, and names that
// actor; once no placement mentions it the delete is accepted.
func TestConfigEditDeleteServerRefusedWhileAnActorPlaces(t *testing.T) {
	t.Parallel()

	doc := configeditServerDoc(t)
	_, err := DeleteServer(doc, "zen")
	wantFieldError(t, err, "", "placed by builder; change their placement in :actors first")

	a := doc.Actors["builder"]
	a.Placement = nil
	doc.Actors["builder"] = a

	edit, err := DeleteServer(doc, "zen")
	if err != nil {
		t.Fatalf("DeleteServer after the placement cleared: %v", err)
	}
	if _, ok := decodeServers(t, edit)["zen"]; ok {
		t.Error("zen is still in the edit's servers section")
	}
	if _, ok := decodeServers(t, edit)["backup"]; !ok {
		t.Error("the delete dropped a server it was not asked to")
	}
}

// Editing one entry rewrites only that entry: the others' bytes survive.
func TestConfigEditEditServerKeepsOthersByteIdentical(t *testing.T) {
	t.Parallel()

	const body = `{
  "backup": {
    "url": "https://backup:7777",
    "fingerprint": "sha256:bbbb"
  },
  "zen": {
    "url": "https://zen:7777",
    "fingerprint": "sha256:aaaa"
  }
}
`
	servers, err := remote.ParseServers([]byte(body))
	if err != nil {
		t.Fatalf("ParseServers: %v", err)
	}
	doc := configeditDoc(t)
	doc.Servers = servers

	edit, err := EditServer(doc, "zen", remote.ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:cccc"})
	if err != nil {
		t.Fatalf("EditServer: %v", err)
	}
	got := string(edit.Sections[config.Servers])
	backup := `  "backup": {
    "url": "https://backup:7777",
    "fingerprint": "sha256:bbbb"
  }`
	if !strings.Contains(got, backup) {
		t.Errorf("editing zen changed backup's bytes:\n%s", got)
	}
	if !strings.Contains(got, `"sha256:cccc"`) || strings.Contains(got, `"sha256:aaaa"`) {
		t.Errorf("the edit did not replace zen's entry:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("the written body must end in a newline: %q", got)
	}
}

// A servers edit carries the servers section alone, never the actors the
// section's guard reads.
func TestConfigEditServerSectionsClosed(t *testing.T) {
	t.Parallel()

	doc := configeditServerDoc(t)
	edit, err := EditServer(doc, "backup", remote.ServerEntry{URL: "https://backup:7777", CA: "system"})
	if err != nil {
		t.Fatalf("EditServer: %v", err)
	}
	if len(edit.Sections) != 1 {
		t.Fatalf("sections = %v, want only servers", edit.Sections)
	}
	if _, ok := edit.Sections[config.Servers]; !ok {
		t.Error("the edit must carry the servers section")
	}
}
