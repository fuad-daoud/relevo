package serve

import (
	"net/http"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestRoundFileGateServesASealedChainCheck pins the round-file resolver's gate
// arm. A served chain runs its acceptance check as a chain step, and that step
// seals the log into a round_file row and removes it from the directory, so a
// member round has nothing at the gate log path. Such a round serves the sealed
// row of the latest check run keyed to it, and a binding outside a chain -- and
// a member round that still has a gate log of its own -- resolves to its gate
// log path as before.
//
// Mutation: drop the chainGateLogPath call from gatePath and the sealed arm
// answers the gate log path, which no file or row answers.
func TestRoundFileGateServesASealedChainCheck(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := createChain(t, env, "shop")
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)

	// Round 2's check log, sealed the way a chain step seals it: the bytes go
	// into the row and the on-disk log goes away.
	checkPath := rt.Store.CheckLogPath("shop", 2, 1)
	if err := os.WriteFile(checkPath, []byte("chain check output\n"), 0o644); err != nil {
		t.Fatalf("write the check log: %v", err)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.ChainCheckPut("shop", db.ChainCheckRow{
			Run: 1, Step: "check", Command: "make check", Result: "green", Log: checkPath,
		}); err != nil {
			return err
		}
		return tx.PutRoundFile("shop", 2, checkPath, []byte("chain check output\n"))
	}); err != nil {
		t.Fatalf("seal the check log: %v", err)
	}
	if err := os.Remove(checkPath); err != nil {
		t.Fatalf("remove the sealed check log: %v", err)
	}

	got, known := roundFilePath(rt, "shop", 2, "gate")
	if !known || got != checkPath {
		t.Fatalf("roundFilePath(shop, 2, gate) = (%q, %v), want the sealed check log %q", got, known, checkPath)
	}
	body, err := rt.Store.ReadFile(got)
	if err != nil || string(body) != "chain check output\n" {
		t.Fatalf("the resolved gate reads %q (err %v), want the sealed check body", body, err)
	}

	// A round the chain never checked keeps the gate log path.
	if got, _ := roundFilePath(rt, "shop", 1, "gate"); got != rt.Store.GateLogPath("shop", 1) {
		t.Errorf("roundFilePath(shop, 1, gate) = %q, want the gate log path of an unchecked round", got)
	}

	// A plain binding's gate log still resolves to its file, sealed or not.
	createAndAbsorb(t, env, "nogate")
	plainGate := rt.Store.GateLogPath("nogate", 1)
	if err := os.WriteFile(plainGate, []byte("make check\nPASS\n"), 0o644); err != nil {
		t.Fatalf("write the gate log: %v", err)
	}
	if got, _ := roundFilePath(rt, "nogate", 1, "gate"); got != plainGate {
		t.Errorf("roundFilePath(nogate, 1, gate) = %q, want the plain binding's gate log %q", got, plainGate)
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("nogate", 1, plainGate, []byte("make check\nPASS\n"))
	}); err != nil {
		t.Fatalf("seal the gate log: %v", err)
	}
	if err := os.Remove(plainGate); err != nil {
		t.Fatalf("remove the sealed gate log: %v", err)
	}
	if got, _ := roundFilePath(rt, "nogate", 1, "gate"); got != plainGate {
		t.Errorf("roundFilePath(nogate, 1, gate) = %q with the gate log sealed, want %q", got, plainGate)
	}
}
