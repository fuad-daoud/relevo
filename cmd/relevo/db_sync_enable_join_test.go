package main

// The enable intake rules: the code, the three secrets and the one stdin they
// share. Like the rest of this package's tests these parse flags and build a
// frame; none of them dials an owner, opens a database or reaches a network.

import (
	"errors"
	"strings"
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

const enableJoinRemote = "libsql://fleet.invalid"

// The tail a client builds and the tail the owner splits are the same
// arrangement. Both ends of that are pinned here because the split runs from the
// end: if either side were reordered, a credential would be stored under another
// one's name and nothing else in the tree would notice.
func TestEnableTailRoundTripsThroughTheFrame(t *testing.T) {
	t.Setenv(relevosync.EnvR2Secret, "R2-FROM-ENV")
	flags := parsedR2Flags(t, "--r2-endpoint", "https://acct.r2.invalid",
		"--r2-bucket", "bodies", "--r2-key-id", "key-id")
	var opts dbSyncVerbOptions
	tail, err := dbSyncEnableR2Tail([]byte("TOKEN"), []byte("PASSPHRASE"), flags, &opts)
	if err != nil {
		t.Fatalf("dbSyncEnableR2Tail: %v", err)
	}
	if string(tail) != "TOKENPASSPHRASER2-FROM-ENV" {
		t.Fatalf("tail = %q, want the token then the passphrase then the bucket secret", tail)
	}
	if opts.R2SecretLen != len("R2-FROM-ENV") || opts.PassphraseLen != len("PASSPHRASE") {
		t.Fatalf("lengths = (%d, %d), want (%d, %d)",
			opts.R2SecretLen, opts.PassphraseLen, len("R2-FROM-ENV"), len("PASSPHRASE"))
	}
	// The last two lengths are what the owner splits from the end with.
	cut := len(tail) - opts.R2SecretLen
	if string(tail[:cut-opts.PassphraseLen]) != "TOKEN" || string(tail[cut-opts.PassphraseLen:cut]) != "PASSPHRASE" {
		t.Errorf("the frame's own lengths do not split the tail back into its three secrets")
	}
}

// One stdin carries one secret. Any two of the three flags that would read one
// are refused, and the refusal names the two that collided rather than saying
// "a secret" -- a reader who passed three needs to know which pair is the
// problem.
func TestEnableRefusesTwoSecretsOnOneStandardInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		token, pass   bool
		r2SecretStdin bool
		want          []string
	}{
		{"token and passphrase", true, true, false, []string{"--token-stdin", "--passphrase-stdin"}},
		{"token and the bucket secret", true, false, true, []string{"--token-stdin", "--r2-secret-stdin"}},
		{"passphrase and the bucket secret", false, true, true, []string{"--passphrase-stdin", "--r2-secret-stdin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{}
			if tc.r2SecretStdin {
				args = append(args, "--r2-secret-stdin")
			}
			err := dbSyncEnableIntake(tc.token, tc.pass, parsedR2Flags(t, args...))
			var got *cliError
			if !errors.As(err, &got) || got.code != codeUsage {
				t.Fatalf("dbSyncEnableIntake = %v, want a usage error", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not name %q", err.Error(), want)
				}
			}
		})
	}
}

// One flag, or none, is fine. The rule is about two secrets on one pipe, not
// about the flags themselves: a plain enable passes no stdin flag at all and a
// --join may take the passphrase on stdin because its token came from the code.
func TestEnableAcceptsOneStdinFlagOrNone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		token, pass bool
		r2          bool
	}{
		{"no flag at all", false, false, false},
		{"the token alone", true, false, false},
		{"the passphrase alone", false, true, false},
		{"the bucket secret alone", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := []string{}
			if tc.r2 {
				args = append(args, "--r2-secret-stdin")
			}
			if err := dbSyncEnableIntake(tc.token, tc.pass, parsedR2Flags(t, args...)); err != nil {
				t.Errorf("dbSyncEnableIntake = %v, want no refusal", err)
			}
		})
	}
}

// The code names the remote and the token, so a joiner needs neither --url nor
// --token-stdin. Both are the code's job when it is given.
func TestAJoinCodeNamesTheRemoteAndTheToken(t *testing.T) {
	t.Parallel()
	code, err := relevosync.NewJoinCode(enableJoinRemote, []byte("FIXTURE-CODE-TOKEN"))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	join, err := dbSyncEnableJoin(code, "")
	if err != nil {
		t.Fatalf("dbSyncEnableJoin: %v", err)
	}
	if join.remote != enableJoinRemote || string(join.token) != "FIXTURE-CODE-TOKEN" {
		t.Errorf("the code resolved to remote %q and token %q, want the two it carries", join.remote, join.token)
	}
	if !join.joining {
		t.Error("a code did not mark the enable as a join")
	}
}

// The code is parsed before anything is dialed, so a code this build cannot read
// is a usage error on the spot rather than a refusal that cost a round trip.
func TestJoinRefusesACodeItCannotReadBeforeDialing(t *testing.T) {
	t.Parallel()
	for _, code := range []string{
		"not-a-code",
		"relevo-join-v2-YWJj",
		"relevo-join-v1-!!!not-base64url!!!",
	} {
		join, err := dbSyncEnableJoin(code, "")
		var got *cliError
		if !errors.As(err, &got) || got.code != codeUsage {
			t.Errorf("dbSyncEnableJoin(%q) = %v, want a usage error", code, err)
		}
		if join.remote != "" || join.token != nil || join.joining {
			t.Errorf("a refused code produced %+v", join)
		}
	}
}

// A --url beside a --join names two remotes. The code is the operator's stated
// destination, so a second one means they meant two things and neither should
// quietly win.
func TestJoinAndURLTogetherAreRefused(t *testing.T) {
	t.Parallel()
	code, err := relevosync.NewJoinCode(enableJoinRemote, []byte("FIXTURE-CODE-TOKEN"))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	_, err = dbSyncEnableJoin(code, "libsql://elsewhere.invalid")
	var got *cliError
	if !errors.As(err, &got) || got.code != codeUsage {
		t.Fatalf("dbSyncEnableJoin with two remotes = %v, want a usage error", err)
	}
}

// Without a code the enable is what it always was: --url names the remote and
// the token still comes from its two routes.
func TestNoJoinCodeLeavesAPlainEnableAlone(t *testing.T) {
	t.Parallel()
	join, err := dbSyncEnableJoin("", "libsql://elsewhere.invalid")
	if err != nil {
		t.Fatalf("dbSyncEnableJoin: %v", err)
	}
	if join.remote != "libsql://elsewhere.invalid" || join.joining || join.token != nil {
		t.Errorf("a plain enable resolved to %+v, want the --url and no code", join)
	}
}

// A code carries the token and carries the only one. A joiner is not handed this
// machine's environment, and a token from elsewhere would reach a remote the
// code never named.
func TestAJoinTakesTheTokensFromTheCodeNotTheEnvironment(t *testing.T) {
	t.Setenv(relevosync.EnvToken, "A-TOKEN-FROM-THE-ENVIRONMENT")
	code, err := relevosync.NewJoinCode(enableJoinRemote, []byte("FIXTURE-CODE-TOKEN"))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	join, err := dbSyncEnableJoin(code, "")
	if err != nil {
		t.Fatalf("dbSyncEnableJoin: %v", err)
	}
	token, err := dbSyncEnableToken(join, false)
	if err != nil {
		t.Fatalf("dbSyncEnableToken: %v", err)
	}
	if string(token) != "FIXTURE-CODE-TOKEN" {
		t.Errorf("a join took the token %q, want the one the code carries", token)
	}
}