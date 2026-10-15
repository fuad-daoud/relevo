package main

// The stamping rules the client key is written under, checked where the write
// happens. The client key is the one secret whose export is conditional, so the
// difference between a person's choice and the program's own bookkeeping is what
// decides whether this machine's identity becomes the fleet's: an unstamped key
// never crosses the log, and a stamped one is the owner saying so deliberately.

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"

)

// stampOf is the write stamp the secrets document would read for one name: the
// claim "a person wrote this here", and the only thing an exporter weighs before
// handing a client key to the fleet.
//
// It is read through the document rather than the raw row because that is the
// reader that decides: a stamp this store wrote under a name the document does
// not carry is not one the document weighs.
func stampOf(t *testing.T, name string) bool {
	t.Helper()
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	stamp, ok, err := rt.Config.SecretsDoc().ItemWriteStamp(name)
	if err != nil {
		t.Fatalf("ItemWriteStamp(%s): %v", name, err)
	}
	return err == nil && ok && stamp > 0
}

// TestAGeneratedClientKeyIsNotStamped pins the rule that matters most: no person
// chose this key. Every machine generates one on its first `relevo config server
// key`, so a stamp here would make whichever machine synced first the fleet's
// client, and every other machine's enrolment would stop matching.
func TestAGeneratedClientKeyIsNotStamped(t *testing.T) {
	initRoot(t)

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	pem, generated, err := ensureClientKey(rt)
	if err != nil {
		t.Fatalf("ensureClientKey: %v", err)
	}
	if !generated {
		t.Fatal("ensureClientKey did not generate a key on an empty machine")
	}
	if len(pem) == 0 {
		t.Fatal("ensureClientKey returned no key")
	}
	if stampOf(t, config.SecretClientKey) {
		t.Error("a generated client key was stamped, so this machine's fresh install would become the fleet's")
	}
}

// TestAClientKeySetByAPersonIsStamped is the other half, and it is the way an
// owner makes one machine's key the fleet's: the write is theirs, so it travels.
// Without the stamp the export filter would withhold the key forever and the
// enrolment line would never travel to a joiner.
func TestAClientKeySetByAPersonIsStamped(t *testing.T) {
	initRoot(t)

	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	pem, _, err := ensureClientKey(rt)
	if err != nil {
		t.Fatalf("ensureClientKey: %v", err)
	}
	// Read first: the write below is the person's, and it has to be the last
	// thing that happens so the stamp is the one under test.
	if stampOf(t, config.SecretClientKey) {
		t.Fatal("the generated key was already stamped, so the case proves nothing")
	}
	if err := rt.Config.As("cli", "config secret set client.key").PutSecret(config.SecretClientKey, pem); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}
	if !stampOf(t, config.SecretClientKey) {
		t.Error("a client key a person set carries no write stamp, so it would never reach a joiner")
	}
}