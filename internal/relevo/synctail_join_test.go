package relevo

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// The tail carries three secrets and each is found again by the split alone. The
// order is fixed at both ends -- the client builds it in one place and the owner
// splits it from the end in one place -- so this is what catches either end
// drifting from the other, which is the only way a credential ends up written
// into the row of a different one.
func TestEnableTailSplitsTheThreeSecretsApart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		token, passphrase, r2 string
	}{
		{"all three", "TOKEN", "PASSPHRASE", "R2SECRET"},
		{"no bucket secret", "TOKEN", "PASSPHRASE", ""},
		{"no passphrase", "TOKEN", "", "R2SECRET"},
		{"token alone", "TOKEN", "", ""},
		{"empty tail", "", "", ""},
		// A passphrase with no token in front of it, and a token with no
		// passphrase after it: the lengths are what place them, so both must
		// come back whole even when the middle is empty.
		{"empty passphrase between the token and the secret", "TOKEN", "", "R2SECRET"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tail := []byte(tc.token + tc.passphrase + tc.r2)
			verb := &wire.SyncVerb{
				Verb:          wire.SyncVerbEnable,
				R2SecretLen:   len(tc.r2),
				PassphraseLen: len(tc.passphrase),
			}
			token, passphrase, r2, err := enableIntake(verb, tail)
			if err != nil {
				t.Fatalf("enableIntake: %v", err)
			}
			if string(token) != tc.token {
				t.Errorf("token = %q, want %q", token, tc.token)
			}
			if string(passphrase) != tc.passphrase {
				t.Errorf("passphrase = %q, want %q", passphrase, tc.passphrase)
			}
			if string(r2.Secret) != tc.r2 {
				t.Errorf("bucket secret = %q, want %q", r2.Secret, tc.r2)
			}
		})
	}
}

// The split runs from the end, so the two lengths must fit the tail together. A
// clamped split would keep part of the passphrase as the bucket secret and store
// a credential under the wrong name, which is why a length past the tail is
// refused rather than adjusted.
func TestEnableIntakeRefusesALengthTheTailCannotHold(t *testing.T) {
	t.Parallel()
	tail := []byte("TOKENPASS")
	for _, tc := range []struct {
		name               string
		secretLen, passLen int
	}{
		{"the bucket secret is longer than the tail", len(tail) + 1, 0},
		{"the passphrase is longer than the tail", 0, len(tail) + 1},
		{"the two lengths together are longer than the tail", 5, 5},
		{"a negative length", -1, 0},
		{"a negative passphrase length", 0, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verb := &wire.SyncVerb{R2SecretLen: tc.secretLen, PassphraseLen: tc.passLen}
			token, passphrase, _, err := enableIntake(verb, tail)
			if err == nil {
				t.Fatalf("enableIntake = (%q, %q, nil), want a refusal", token, passphrase)
			}
			if !errors.Is(err, db.ErrInvalid) {
				t.Errorf("err = %v, want it to wrap db.ErrInvalid", err)
			}
		})
	}
}

// The bucket flags travel on the header and are paired with the secret the tail
// carried, so a machine with nothing stored ends up holding all four.
func TestEnableIntakePairsTheSecretWithTheFrameBucketFlags(t *testing.T) {
	t.Parallel()
	verb := &wire.SyncVerb{
		R2Endpoint:  "https://acct.r2.invalid",
		R2Bucket:    "bodies",
		R2KeyID:     "key-id",
		R2SecretLen: len("R2SECRET"),
	}
	_, _, r2, err := enableIntake(verb, []byte("TOKENR2SECRET"))
	if err != nil {
		t.Fatalf("enableIntake: %v", err)
	}
	if r2.Endpoint != "https://acct.r2.invalid" || r2.Bucket != "bodies" || r2.KeyID != "key-id" {
		t.Errorf("bucket flags = %+v, want the three the frame named", r2)
	}
	if string(r2.Secret) != "R2SECRET" {
		t.Errorf("bucket secret = %q, want the tail's last %d bytes", r2.Secret, verb.R2SecretLen)
	}
}