package main

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

func parsedR2Flags(t *testing.T, args ...string) dbSyncR2Flags {
	t.Helper()
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	var v dbSyncR2Flags
	installR2Flags(fs, &v)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return v
}

// The three bucket flags reach the frame and the secret is the tail's last
// R2SecretLen bytes, after the token; stdin beats the environment.
func TestEnableFrameCarriesTheR2FlagsAndSecret(t *testing.T) {
	t.Setenv(relevosync.EnvR2Secret, "from-env")
	prev := dbSyncR2SecretStdin
	t.Cleanup(func() { dbSyncR2SecretStdin = prev })

	for _, tc := range []struct {
		name, stdin, want string
		args              []string
	}{
		{"env", "", "from-env", nil},
		{"stdin", "from-stdin\n", "from-stdin", []string{"--r2-secret-stdin"}},
	} {
		dbSyncR2SecretStdin = testVerbStdinReader(t, tc.stdin)
		flags := parsedR2Flags(t, append([]string{"--r2-endpoint", "https://e", "--r2-bucket", "b", "--r2-key-id", "k"}, tc.args...)...)
		var opts dbSyncVerbOptions
		tail, err := dbSyncEnableR2Tail([]byte("TOKEN"), flags, &opts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(tail) != "TOKEN"+tc.want || opts.R2SecretLen != len(tc.want) {
			t.Errorf("%s: tail %q with secret length %d, want the token then %q", tc.name, tail, opts.R2SecretLen, tc.want)
		}
		if opts.R2Endpoint != "https://e" || opts.R2Bucket != "b" || opts.R2KeyID != "k" {
			t.Errorf("%s: frame fields = %+v, want the three flags", tc.name, opts)
		}
	}
}

func TestEnableWithNoSecretSendsTheTokenAlone(t *testing.T) {
	t.Setenv(relevosync.EnvR2Secret, "")
	var opts dbSyncVerbOptions
	tail, err := dbSyncEnableR2Tail([]byte("TOKEN"), parsedR2Flags(t), &opts)
	if err != nil || string(tail) != "TOKEN" || opts.R2SecretLen != 0 {
		t.Fatalf("tail = %q, length %d, err %v; want the token alone", tail, opts.R2SecretLen, err)
	}
}

func TestEnableRefusesAnEmptySecretOnStdin(t *testing.T) {
	prev := dbSyncR2SecretStdin
	t.Cleanup(func() { dbSyncR2SecretStdin = prev })
	dbSyncR2SecretStdin = testVerbStdinReader(t, "\n")
	var opts dbSyncVerbOptions
	_, err := dbSyncEnableR2Tail(nil, parsedR2Flags(t, "--r2-secret-stdin"), &opts)
	var got *cliError
	if !errors.As(err, &got) || got.code != codeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

// Missing bucket credentials are the reader's to supply, so they read as usage
// naming the flags, never as an internal failure.
func TestNoR2RefusalIsUsage(t *testing.T) {
	t.Parallel()
	classified := dbSyncVerbRefusal(wire.SyncVerbEnable, &client.VerbRefusal{
		Code:    wire.SyncCodeNoR2,
		Message: "sync: no R2 credentials on this machine: pass --r2-endpoint",
	})
	var got *cliError
	if !errors.As(classified, &got) || got.code != codeUsage {
		t.Fatalf("classified = %v, want a usage error", classified)
	}
	if !strings.Contains(got.Error(), "--r2-endpoint") {
		t.Errorf("the refusal %q dropped the flags it names", got.Error())
	}
}
