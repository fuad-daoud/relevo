package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

// parsePushArgs runs push's flag set over args the way cmdPush does and returns
// what pushAckArgs made of the positionals. Pure: no runtime is built, nothing
// is spawned and nothing reaches the network.
func parsePushArgs(t *testing.T, args []string) (*pushFlagValues, []string, error) {
	t.Helper()
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	v := pushFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return v, nil, err
	}
	return v, fs.Args(), nil
}

// TestPushAckUsageShapes covers the refusals that must happen before any runtime
// exists: a wrong number of positionals, a non-integer seq, and a positional
// given to the long form.
func TestPushAckUsageShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"ack without a seq", []string{"--ack", "webshop"}},
		{"ack with two seqs", []string{"--ack", "webshop", "1", "2"}},
		{"non-integer seq", []string{"--ack", "webshop", "one"}},
		{"positional without ack", []string{"webshop", "1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, rest, err := parsePushArgs(t, c.args)
			if err != nil {
				t.Fatalf("parseFlags(%v) = %v, want the ack check to decide", c.args, err)
			}
			ackErr := pushAckRejected(t, v, rest)
			var ce *cliError
			if !errors.As(ackErr, &ce) {
				t.Fatalf("pushAckArgs error = %v, want a coded error", ackErr)
			}
			if ce.code != codeUsage {
				t.Errorf("code = %q, want usage", ce.code)
			}
			if ce.message == "" {
				t.Error("message is empty")
			}
		})
	}
}

// pushAckRejected returns the usage error pushAckArgs produced, failing the test
// if it accepted the call instead.
func pushAckRejected(t *testing.T, v *pushFlagValues, rest []string) error {
	t.Helper()
	_, _, acking, err := pushAckArgs(v, rest)
	if err == nil && !acking {
		t.Fatal("pushAckArgs did not confirm and did not refuse: want a usage error")
	}
	return err
}

// TestPushAckArgsAcceptsTheDocumentedShape: the one form that is not a usage
// error parses to a binding and a seq.
func TestPushAckArgsAcceptsTheDocumentedShape(t *testing.T) {
	t.Parallel()

	v, rest, err := parsePushArgs(t, []string{"--ack", "webshop", "7"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	binding, seq, acking, ackErr := pushAckArgs(v, rest)
	if ackErr != nil {
		t.Fatalf("pushAckArgs: %v", ackErr)
	}
	if !acking || binding != "webshop" || seq != 7 {
		t.Errorf("pushAckArgs = %q %d acking=%v, want webshop 7 acking=true", binding, seq, acking)
	}
}

// TestPushLongFormTakesNoPositionals: `relevo push webshop` is a usage error,
// not a binding the holder drains.
func TestPushLongFormTakesNoPositionals(t *testing.T) {
	t.Parallel()

	v, rest, err := parsePushArgs(t, []string{"webshop"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if _, _, acking, ackErr := pushAckArgs(v, rest); acking || ackErr == nil {
		t.Errorf("pushAckArgs = acking %v err %v, want a usage error for a positional", acking, ackErr)
	}
}

// TestPushLongFormWithoutArgsIsNotAnAck: the bare long form is the holder, not
// an ack, so nothing is confirmed and no usage error is raised.
func TestPushLongFormWithoutArgsIsNotAnAck(t *testing.T) {
	t.Parallel()

	v, rest, err := parsePushArgs(t, []string{"--mastermind", "pl_aaaaaaaabbbb"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if _, _, acking, ackErr := pushAckArgs(v, rest); acking || ackErr != nil {
		t.Errorf("pushAckArgs = acking %v err %v, want the long form", acking, ackErr)
	}
}
