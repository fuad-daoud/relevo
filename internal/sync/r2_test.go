package sync

// The R2 credentials and the rule that decides whether an enable may go ahead
// with them. The rule is a pure function of its input so it can be tested
// without a database and without the CLI: a value the resolve reads is the
// value it was given, and a refusal is the same line whatever the machine
// happened to hold.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// r2Pair opens a machine-local file to store credentials in.
func r2Pair(t *testing.T) Local {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return local
}

// TestR2SecretsRoundTripThroughTheLocalFile pins the store and read: what an
// enable writes is what the handshake is later built from, byte for byte.
//
// The endpoint's trailing space is what makes this more than a round trip: the
// value is trimmed on the way out, because a secret read from a file a human
// edited by hand carries whatever whitespace was around it.
func TestR2SecretsRoundTripThroughTheLocalFile(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	now := time.Unix(0, 0).UTC()
	want := R2Secrets{
		Endpoint: "https://acct.r2.cloudflarestorage.com",
		Bucket:   "relevo-sync",
		KeyID:    "key-1",
		Secret:   "secret-1",
	}
	if err := SetR2(local, want, now); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	got, err := ReadR2(local)
	if err != nil {
		t.Fatalf("ReadR2: %v", err)
	}
	if got != want {
		t.Errorf("ReadR2 = %+v, want %+v", got, want)
	}

	if err := DeleteR2(local); err != nil {
		t.Fatalf("DeleteR2: %v", err)
	}
	after, err := ReadR2(local)
	if err != nil {
		t.Fatalf("ReadR2 after delete: %v", err)
	}
	if after.Complete() || len(after.Missing()) != 4 {
		t.Errorf("ReadR2 after delete = %+v, want nothing configured", after)
	}
	// A delete on a machine that never configured R2 is still clean: the disable
	// path runs it unconditionally.
	if err := DeleteR2(local); err != nil {
		t.Errorf("a second DeleteR2 refused: %v", err)
	}
}

// TestSetR2RefusesAnIncompleteSet pins that a half-written configuration is
// refused at the door rather than stored: a machine holding three of four would
// read as configured and fail at the first upload instead.
func TestSetR2RefusesAnIncompleteSet(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	now := time.Unix(0, 0).UTC()
	partial := r2Fixture
	partial.Secret = ""

	err := SetR2(local, partial, now)
	if !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("SetR2 = %v, want a refusal for an incomplete set", err)
	}
	if got, err := ReadR2(local); err != nil || got.Complete() {
		t.Errorf("a refused set was stored anyway: %+v (err %v)", got, err)
	}
}

// TestEnableRefusesWithoutR2 is the preflight rule, tested as the pure function
// it is: a machine holding no credentials cannot be enabled, and a machine
// holding all four can. The refusal names the flags that clear it, because a
// reader who was expecting a working enable needs to know what to pass.
//
// The half-configured case is the one that matters: three of four is not a
// usable credential, so it refuses the same way an empty machine does rather
// than letting an enable get as far as the first upload.
func TestEnableRefusesWithoutR2(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		stored  R2Secrets
		intake  R2Intake
		wantErr bool
	}{
		{
			name:    "a machine holding nothing is refused",
			wantErr: true,
		},
		{
			name:   "a complete stored set is enough on its own",
			stored: r2Fixture,
		},
		{
			name:   "the flags alone are enough",
			intake: R2Intake{Endpoint: "https://e", Bucket: "b", KeyID: "k", Secret: []byte("s")},
		},
		{
			name:   "a half-configured machine is refused however it was filled",
			stored: R2Secrets{Endpoint: "https://e", Bucket: "b", KeyID: "k"},
			intake: R2Intake{Secret: []byte("s")},
			// The two halves together are complete, so this one passes: what is
			// refused is a machine still missing something once both routes have
			// been consulted, not a machine whose halves differ.
		},
		{
			name:    "a machine still missing the secret is refused",
			stored:  R2Secrets{Endpoint: "https://e", Bucket: "b", KeyID: "k"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			intake := tc.intake
			intake.Stored = tc.stored
			got, err := intake.R2Resolve()
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("R2Resolve = %+v, want a refusal", got)
			case tc.wantErr:
				if !errors.Is(err, ErrNoR2) {
					t.Errorf("refusal = %v, want ErrNoR2", err)
				}
				// The message has to name the flags: a refusal the reader cannot
				// act on is a bug report, not a fix.
				for _, flag := range []string{"--r2-endpoint", "--r2-bucket", "--r2-key-id", EnvR2Secret} {
					if !stringsContains(err.Error(), flag) {
						t.Errorf("the refusal %q does not name %s", err, flag)
					}
				}
			case err != nil:
				t.Fatalf("R2Resolve: %v", err)
			case !got.Complete():
				t.Errorf("R2Resolve = %+v, want a complete set", got)
			}
		})
	}
}

// TestR2ResolveTakesAFlagOverTheStoredValue pins the precedence: a flag wins,
// and a field with no flag falls back to what is stored. It is what lets a
// machine be pointed at another bucket without its access key being reissued.
func TestR2ResolveTakesAFlagOverTheStoredValue(t *testing.T) {
	t.Parallel()
	stored := r2Fixture
	got, err := R2Intake{
		Bucket: "another-bucket",
		Stored: stored,
	}.R2Resolve()
	if err != nil {
		t.Fatalf("R2Resolve: %v", err)
	}
	if got.Bucket != "another-bucket" {
		t.Errorf("bucket = %q, want the flag's value", got.Bucket)
	}
	if got.KeyID != stored.KeyID || got.Secret != stored.Secret || got.Endpoint != stored.Endpoint {
		t.Errorf("R2Resolve = %+v, want the stored values for the flags not passed", got)
	}
}

// TestDisableDeletesTheR2CredentialsWithTheToken pins that the credentials go
// when sync is turned off. A machine marked off holding a live bucket key is one
// the next enable would inherit credentials for without being asked.
func TestDisableDeletesTheR2CredentialsWithTheToken(t *testing.T) {
	t.Parallel()
	local := r2Pair(t)
	now := time.Unix(0, 0).UTC()
	if err := SetToken(local, []byte("token-value"), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := SetR2(local, r2Fixture, now); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	d := &Disabler{Local: local, Now: func() time.Time { return now }}
	if _, err := d.Disable(t.Context()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got, err := ReadR2(local); err != nil || got.Complete() {
		t.Errorf("the R2 credentials survived the disable: %+v (err %v)", got, err)
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("the token survived the disable")
	}
}

// stringsContains is strings.Contains without the import, used where a refusal's
// text is the thing under test.
func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
