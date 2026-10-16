package main

// The rekey verb's CLI shape. The passphrase rules are pure functions over the
// injected sources, so nothing here reads a real terminal, spawns a worker or
// reaches the network -- the same rule unlock's tests run under.

import (
	"strings"
	"testing"
)

// withRekeySources installs the passphrase sources for one test. The new
// passphrase shares the environment fake with the current one, read under its
// own name, so a test stages both.
func withRekeySources(t *testing.T, stdin, env, newEnv string, tty func(string) ([]byte, bool, error)) {
	t.Helper()
	withPassphraseSources(t, stdin, env, tty)
	oldNew := dbSyncPassphraseEnvFn
	t.Cleanup(func() { dbSyncPassphraseEnvFn = oldNew })
	current := dbSyncPassphraseEnvFn
	dbSyncPassphraseEnvFn = func(name string) string {
		if name == dbSyncNewPassphraseEnv {
			return newEnv
		}
		return current(name)
	}
}

// TestRekeyReadsTheCurrentFromStdinAndTheNewFromItsEnv pins the intake order:
// the current passphrase follows unlock's order -- stdin, then its environment,
// then a prompt -- and the new one has no stdin route at all.
func TestRekeyReadsTheCurrentFromStdinAndTheNewFromItsEnv(t *testing.T) {
	withRekeySources(t, "current-secret\n", "unused", "new-secret", ttyAnswering("tty-answer"))
	current, newPass, err := dbSyncRekeyPassphrases(true)
	if err != nil || string(current) != "current-secret" || string(newPass) != "new-secret" {
		t.Fatalf("the pair = (%q, %q, %v), want stdin then the new env", current, newPass, err)
	}
}

// TestRekeyReadsTheNewFromTTYAskedTwice pins the prompt route: the new
// passphrase is asked for twice and the answers must agree, on the same rule a
// new unlock's is.
func TestRekeyReadsTheNewFromTTYAskedTwice(t *testing.T) {
	withRekeySources(t, "", "", "", ttyAnswering("tty-current", "tty-new", "tty-new"))
	current, newPass, err := dbSyncRekeyPassphrases(false)
	if err != nil || string(current) != "tty-current" || string(newPass) != "tty-new" {
		t.Fatalf("the pair = (%q, %q, %v), want the prompted pair", current, newPass, err)
	}

	withRekeySources(t, "", "", "", ttyAnswering("first", "second"))
	if _, _, err := dbSyncRekeyPassphrases(false); err == nil {
		t.Fatal("a mismatched repeat was accepted")
	} else if strings.Contains(err.Error(), "first") || strings.Contains(err.Error(), "second") {
		t.Errorf("the refusal echoes a passphrase: %v", err)
	}
}

// TestRekeyRefusesAnEmptyPassphrase pins the empty rules: either half empty is
// refused, which is what keeps a rotation to the empty string from ever
// happening by accident of an unset variable.
func TestRekeyRefusesAnEmptyPassphrase(t *testing.T) {
	withRekeySources(t, "\n", "", "new-secret", ttyAnswering("x"))
	if _, _, err := dbSyncRekeyPassphrases(true); err == nil {
		t.Fatal("an empty current passphrase was accepted")
	}
	withRekeySources(t, "current-secret\n", "", "", ttyAnswering("first", "second"))
	if _, _, err := dbSyncRekeyPassphrases(true); err == nil {
		t.Fatal("an empty new passphrase was accepted")
	}
}

// TestRekeyRefusesTheCurrentPassphraseAsTheNewOne pins the rotation rule at the
// intake: the new passphrase may not be the current one, because the same
// passphrase against the new public salt lets a leak re-derive the same key.
func TestRekeyRefusesTheCurrentPassphraseAsTheNewOne(t *testing.T) {
	withRekeySources(t, "same-secret\n", "", "same-secret", ttyAnswering("x"))
	if _, _, err := dbSyncRekeyPassphrases(true); err == nil {
		t.Fatal("a rotation to the current passphrase was accepted")
	} else if strings.Contains(err.Error(), "same-secret") {
		t.Errorf("the refusal echoes a passphrase: %v", err)
	}
}

// TestRekeyIsInTheUsage pins that the verb's usage line names it, so a reader
// listing the sync verbs finds the rotation on the same screen as unlock.
func TestRekeyIsInTheUsage(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"relevo db sync rekey", dbSyncNewPassphraseEnv, "RELEVO_SYNC_PASSPHRASE"} {
		if !strings.Contains(dbSyncUsage, want) {
			t.Errorf("the usage text does not mention %q:\n%s", want, dbSyncUsage)
		}
	}
}
