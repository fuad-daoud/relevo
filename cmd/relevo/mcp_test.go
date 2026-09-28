package main

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/mcp"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestMCPResolveKindAndMode pins the flag rules: only the empty kind and
// opencode exist, and an opencode server always serves tools, never the Claude
// channel.
func TestMCPResolveKindAndMode(t *testing.T) {
	if kind, err := mcpResolveKind(""); err != nil || kind != "" {
		t.Errorf("mcpResolveKind(\"\") = %q, %v, want empty", kind, err)
	}
	if kind, err := mcpResolveKind("opencode"); err != nil || kind != "opencode" {
		t.Errorf("mcpResolveKind(opencode) = %q, %v", kind, err)
	}
	if _, err := mcpResolveKind("claude"); err == nil {
		t.Error("claude must not be accepted as a server kind")
	}

	if mode, err := mcpResolveMode("opencode", "auto"); err != nil || mode != mcp.ModeTools {
		t.Errorf("opencode auto mode = %v, %v, want tools", mode, err)
	}
	if _, err := mcpResolveMode("opencode", "channel"); err == nil {
		t.Error("an opencode server must refuse --mode channel")
	}
	if mode, err := mcpResolveMode("", "tools"); err != nil || mode != mcp.ModeTools {
		t.Errorf("claude tools mode = %v, %v", mode, err)
	}
}

// TestOpencodeSessionMasterMind pins the per-call resolver: a known session
// yields its record's id, and a missing record's error names the fix.
func TestOpencodeSessionMasterMind(t *testing.T) {
	reg := mastermindRegistryAt(t, t.TempDir())
	rec, err := reg.Create(mastermind.Record{
		ID: "mm_aaaaaaaaaaaa", Name: "opencode-1", HarnessKind: "opencode",
		SessionID: "ses_abc123", CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolve := opencodeSessionMasterMind(relevo.Runtime{MasterMinds: reg})
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
}
