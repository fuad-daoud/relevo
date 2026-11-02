package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestMCPResolveKind pins the flag rule: only the empty kind and opencode
// exist as a server kind.
func TestMCPResolveKind(t *testing.T) {
	if kind, err := mcpResolveKind(""); err != nil || kind != "" {
		t.Errorf("mcpResolveKind(\"\") = %q, %v, want empty", kind, err)
	}
	if kind, err := mcpResolveKind("opencode"); err != nil || kind != "opencode" {
		t.Errorf("mcpResolveKind(opencode) = %q, %v", kind, err)
	}
	if _, err := mcpResolveKind("claude"); err == nil {
		t.Error("claude must not be accepted as a server kind")
	}
}

// TestOpencodeSessionMasterMind pins the per-call resolver: a known session
// yields its record's id, a missing record's error names the fix, and a nil
// registry is refused instead of silently yielding no resolver.
func TestOpencodeSessionMasterMind(t *testing.T) {
	if resolve, err := opencodeSessionMasterMind(relevo.Runtime{}); err == nil {
		t.Errorf("opencodeSessionMasterMind(nil registry) error = %v, want an error", err)
	} else if resolve != nil {
		t.Error("a nil registry must not yield a resolver")
	}

	reg := mastermindRegistryAt(t, t.TempDir())
	rec, err := reg.Create(mastermind.Record{
		ID: "mm_aaaaaaaaaaaa", Name: "opencode-1", HarnessKind: "opencode",
		SessionID: "ses_abc123", CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolve, err := opencodeSessionMasterMind(relevo.Runtime{MasterMinds: reg})
	if err != nil {
		t.Fatalf("opencodeSessionMasterMind: %v", err)
	}
	if resolve == nil {
		t.Fatal("a registry must yield a resolver")
	}
	id, err := resolve("ses_abc123")
	if err != nil || id != rec.ID {
		t.Errorf("resolve(ses_abc123) = %q, %v, want %q", id, err, rec.ID)
	}

	_, err = resolve("ses_other")
	if err == nil || !strings.Contains(err.Error(), "relevo mastermind enable --repo") {
		t.Errorf("resolve(ses_other) error = %v, want it to name the fix", err)
	}

	if _, err := resolve(""); err == nil || err.Error() != "this call carries no OpenCode session (`_meta ai.opencode/sessionID`); cannot tell which MasterMind it belongs to" {
		t.Errorf("resolve(\"\") error = %v, want the sessionless refusal", err)
	}
}

// TestMCPVerbs pins the startup shape: an opencode server needs a mastermind
// registry and is refused without one, while a Claude server stays tools-only
// with no per-call resolver.
func TestMCPVerbs(t *testing.T) {
	if _, err := mcpVerbs(relevo.Runtime{}, "opencode", "", ""); err == nil || !strings.Contains(err.Error(), "no mastermind registry") {
		t.Errorf("mcpVerbs(opencode, nil registry) error = %v, want it to name the registry", err)
	}

	reg := mastermindRegistryAt(t, t.TempDir())
	v, err := mcpVerbs(relevo.Runtime{MasterMinds: reg}, "opencode", "", "")
	if err != nil {
		t.Fatalf("mcpVerbs(opencode, registry) error = %v", err)
	}
	if v.ResolveSession == nil {
		t.Error("an opencode server with a registry must resolve tool-call sessions")
	}

	v, err = mcpVerbs(relevo.Runtime{}, "", "", "")
	if err != nil {
		t.Fatalf("mcpVerbs(Claude, nil registry) error = %v, want nil (tools-only startup unchanged)", err)
	}
	if v.ResolveSession != nil {
		t.Error("a Claude server must not carry a per-call resolver")
	}
}

// TestMCPVerbsClaudeResolvesLateRecord pins the late-hook path: a Claude server
// that starts with no identity answers the init error until its record appears,
// then resolves to it and pins it without re-hitting the registry.
func TestMCPVerbsClaudeResolvesLateRecord(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-late-mcp")
	t.Setenv("CLAUDE_PID", "4242")
	t.Setenv("RELEVO_MASTERMIND", "")
	t.Setenv("RELEVO_PLANNER", "")

	reg := mastermindRegistryAt(t, t.TempDir())
	procCalls := 0
	rt := relevo.Runtime{
		MasterMinds: reg,
		ProcStart: func(int) (int64, error) {
			procCalls++
			return 0, errors.New("no proc in test")
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	}
	v, err := mcpVerbs(rt, "", "", "")
	if err != nil {
		t.Fatalf("mcpVerbs(Claude, registry) error = %v", err)
	}
	if v.ResolveSession == nil {
		t.Fatal("a Claude server with a registry and no startup id must re-resolve per call")
	}

	if _, err := v.ResolveSession(""); err == nil || !strings.Contains(err.Error(), "relevo mastermind init") {
		t.Fatalf("resolve before the record = %v, want the init error", err)
	}

	rec, err := reg.Create(mastermind.Record{
		ID: "mm_aaaaaaaaaaaa", Name: "claude-1", HarnessKind: "claude",
		SessionID: "sess-late-mcp", CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, err := v.ResolveSession("")
	if err != nil || id != rec.ID {
		t.Fatalf("resolve after the record = %q, %v, want %q", id, err, rec.ID)
	}

	callsAfterHit := procCalls
	if err := reg.Forget(rec.ID, func(string) bool { return false }); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	id, err = v.ResolveSession("")
	if err != nil || id != rec.ID {
		t.Fatalf("cached resolve = %q, %v, want %q without the registry", id, err, rec.ID)
	}
	if procCalls != callsAfterHit {
		t.Errorf("cached resolve hit ProcStart %d times, want no new hit (was %d)", procCalls, callsAfterHit)
	}

	bad, err := mcpVerbs(rt, "", "", "ghost")
	if err != nil {
		t.Fatalf("mcpVerbs with flag: %v", err)
	}
	if _, err := bad.ResolveSession(""); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("resolve with an unknown flag = %v, want the lookup error to propagate", err)
	}

	started, err := mcpVerbs(rt, "", rec.ID, "")
	if err != nil {
		t.Fatalf("mcpVerbs with startup id: %v", err)
	}
	if started.ResolveSession != nil {
		t.Error("a Claude server that started with an identity must not re-resolve")
	}
}
