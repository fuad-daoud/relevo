package sync

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const joinCodeToken = "FIXTURE-JOIN-TOKEN-0123456789abcdef"

// The code carries the two things a joiner cannot get any other way, and a
// round trip is the whole of its contract: an enable parses it before it dials
// anything, so this is the only proof that the line a machine prints is the line
// a new machine reads.
func TestJoinCodeRoundTripsTheRemoteAndToken(t *testing.T) {
	t.Parallel()
	code, err := NewJoinCode("libsql://fleet.invalid", []byte(joinCodeToken))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	got, err := ParseJoinCode(code)
	if err != nil {
		t.Fatalf("ParseJoinCode(%q): %v", code, err)
	}
	if got.URL != "libsql://fleet.invalid" || got.Token != joinCodeToken {
		t.Errorf("round trip = %+v, want the remote and the token", got)
	}
}

// A code is meant to survive being copied by hand, so the characters a paste is
// most likely to mangle must not appear in it at all. This is what pins the
// alphabet to unpadded base64url rather than the padded standard encoding.
func TestJoinCodeUsesTheCopyPasteAlphabet(t *testing.T) {
	t.Parallel()
	code, err := NewJoinCode("libsql://fleet.invalid", []byte(joinCodeToken))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	for _, banned := range []string{"+", "/", "=", " ", "\t", "\n", "\r"} {
		if strings.Contains(code, banned) {
			t.Errorf("the code %q carries %q, which a copy-paste mangles", code, banned)
		}
	}
	if !strings.HasPrefix(code, JoinCodePrefix) {
		t.Errorf("the code %q does not start with %q", code, JoinCodePrefix)
	}
}

// The trailing newline a shell or a chat window adds is not part of what the
// writer meant, so a pasted code is trimmed rather than refused.
func TestParseJoinCodeAcceptsACodePastedWithSurroundingWhitespace(t *testing.T) {
	t.Parallel()
	code, err := NewJoinCode("libsql://fleet.invalid", []byte(joinCodeToken))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	if _, err := ParseJoinCode("  " + code + "\n"); err != nil {
		t.Fatalf("ParseJoinCode on a pasted code: %v", err)
	}
}

// The version is the prefix, so a code written by a build that reads a different
// one is refused before its body is decoded. A body whose layout changed must
// never be read as though this build knew what its fields mean.
func TestParseJoinCodeRefusesAVersionItDoesNotRead(t *testing.T) {
	t.Parallel()
	code, err := NewJoinCode("libsql://fleet.invalid", []byte(joinCodeToken))
	if err != nil {
		t.Fatalf("NewJoinCode: %v", err)
	}
	body := strings.TrimPrefix(code, JoinCodePrefix)
	future := "relevo-join-v2-" + body
	_, err = ParseJoinCode(future)
	if !errors.Is(err, ErrJoinCode) {
		t.Fatalf("ParseJoinCode(%q) = %v, want ErrJoinCode", future, err)
	}
	if !strings.Contains(err.Error(), "v2") {
		t.Errorf("the refusal %q does not name the version it refused", err)
	}
}

// A string that is not a code is refused, not decoded and hoped at: this is what
// stops an operator pasting the wrong thing into --join.
func TestParseJoinCodeRefusesSomethingThatIsNotACode(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"",
		"libsql://fleet.invalid",
		"relevo-join-",
		"relevo-join-v1-",
		"relevo-join-v1-not base64url!",
		JoinCodePrefix + base64.RawURLEncoding.EncodeToString([]byte("not json")),
	} {
		if _, err := ParseJoinCode(in); !errors.Is(err, ErrJoinCode) {
			t.Errorf("ParseJoinCode(%q) = %v, want ErrJoinCode", in, err)
		}
	}
}

// The decoder is closed. A code carrying a field this build does not know is
// refused rather than read as though the field were not there.
func TestParseJoinCodeRefusesACodeCarryingAFieldItDoesNotKnow(t *testing.T) {
	t.Parallel()
	body := `{"url":"libsql://fleet.invalid","token":"` + joinCodeToken + `","passphrase":"stolen"}`
	code := JoinCodePrefix + base64.RawURLEncoding.EncodeToString([]byte(body))
	_, err := ParseJoinCode(code)
	if !errors.Is(err, ErrJoinCode) {
		t.Fatalf("ParseJoinCode on a code with an extra field = %v, want ErrJoinCode", err)
	}
}

// A code naming a remote but no token, or the reverse, cannot point a joiner
// anywhere, so it is refused at the reader rather than failing on the machine it
// was meant for.
func TestParseJoinCodeRefusesAHalfCarriedCode(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"url":"libsql://fleet.invalid","token":""}`,
		`{"url":"","token":"` + joinCodeToken + `"}`,
	} {
		code := JoinCodePrefix + base64.RawURLEncoding.EncodeToString([]byte(body))
		if _, err := ParseJoinCode(code); !errors.Is(err, ErrJoinCode) {
			t.Errorf("ParseJoinCode on %s = %v, want ErrJoinCode", body, err)
		}
	}
}

// A machine with no remote or no token has nothing to print, and printing an
// empty code would hand the reader a line that fails on the machine it is meant
// for.
func TestNewJoinCodeRefusesAnEmptyRemoteOrToken(t *testing.T) {
	t.Parallel()
	if _, err := NewJoinCode("", []byte(joinCodeToken)); err == nil {
		t.Error("NewJoinCode with no remote returned a code")
	}
	if _, err := NewJoinCode("libsql://fleet.invalid", nil); err == nil {
		t.Error("NewJoinCode with no token returned a code")
	}
	if _, err := NewJoinCode("libsql://fleet.invalid", []byte("  \n")); err == nil {
		t.Error("NewJoinCode with a blank token returned a code")
	}
}

// The version segment is the attacker's text: the refusal quotes a bounded,
// inert rendering of it rather than the raw bytes.
func TestParseJoinCodeRendersAHostileVersionInert(t *testing.T) {
	t.Parallel()
	for name, version := range map[string]string{
		"escape and newline": "v2\x1b[2J\nforged line",
		"long":               strings.Repeat("v", 5000),
	} {
		_, err := ParseJoinCode("relevo-join-" + version + "-YWJj")
		if !errors.Is(err, ErrJoinCode) {
			t.Fatalf("%s: ParseJoinCode = %v, want ErrJoinCode", name, err)
		}
		msg := err.Error()
		if len(msg) > 300 {
			t.Errorf("%s: the refusal is %d bytes long", name, len(msg))
		}
		for _, r := range msg {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s: the refusal %q carries control byte %#x", name, msg, r)
			}
		}
	}
}
