package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// declaredCodes reads the const block out of clierror.go and returns every
// code string it declares. The frame is guarded from the source rather than
// from a second hand-kept list, so adding a const without a catalog row (or
// the reverse) fails here.
func declaredCodes(t *testing.T) []string {
	t.Helper()

	src, err := os.ReadFile("clierror.go")
	if err != nil {
		t.Fatalf("read clierror.go: %v", err)
	}
	block := src
	if i := bytes.Index(src, []byte("const (")); i >= 0 {
		if j := bytes.Index(src[i:], []byte("\n)")); j >= 0 {
			block = src[i : i+j]
		}
	} else {
		t.Fatal("clierror.go declares no const block")
	}

	re := regexp.MustCompile(`errorCode\s*=\s*"([a-z_]+)"`)
	var codes []string
	for _, m := range re.FindAllSubmatch(block, -1) {
		codes = append(codes, string(m[1]))
	}
	if len(codes) == 0 {
		t.Fatal("the const block declares no error codes")
	}
	return codes
}

// TestCatalogCoversEveryDeclaredCode pins the two-way rule: every declared
// code has a catalog row and every catalog row is declared, each code is
// lowercase snake, and the exit is 2 for the two usage-shaped codes and 1 for
// the rest.
func TestCatalogCoversEveryDeclaredCode(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	seen := map[errorCode]bool{}
	for _, c := range declaredCodes(t) {
		code := errorCode(c)
		seen[code] = true
		if !snake.MatchString(c) {
			t.Errorf("code %q is not lowercase snake", c)
		}
		entry, ok := catalog[code]
		if !ok {
			t.Errorf("code %q has no catalog row", c)
			continue
		}
		wantExit := 1
		if code == codeUsage || code == codeRefused {
			wantExit = 2
		}
		if entry.exit != wantExit {
			t.Errorf("code %q exit = %d, want %d", c, entry.exit, wantExit)
		}
	}
	for code := range catalog {
		if !seen[code] {
			t.Errorf("catalog row %q is not declared in the const block", code)
		}
	}
}

// TestCatalogNextHints pins the rows whose next command is known: the
// not-found family, the missing daemon and an active gate.
func TestCatalogNextHints(t *testing.T) {
	for _, code := range []errorCode{
		codeBindingNotFound,
		codeRoundNotFound,
		codeArtifactNotFound,
		codeNoDaemon,
		codeGateActive,
	} {
		entry, ok := catalog[code]
		if !ok {
			t.Errorf("code %q has no catalog row", code)
			continue
		}
		if entry.next == "" {
			t.Errorf("code %q has no next hint", code)
		}
	}
	// A hint that is not a command would not help anyone; the ones we carry
	// all name the binary that runs them.
	for code, entry := range catalog {
		if entry.next != "" && !strings.HasPrefix(entry.next, "relevo ") {
			t.Errorf("code %q next = %q, want a relevo command", code, entry.next)
		}
	}
}

// TestReportHumanErrorLine pins the human shape: the code first, the message
// after it, and the next command on its own indented line when there is one.
func TestReportHumanErrorLine(t *testing.T) {
	var buf bytes.Buffer
	code := report(&buf, failNext(codeBindingNotFound, "relevo bind", "no binding for /tmp/x"), false)
	if code != 1 {
		t.Errorf("report exit = %d, want 1", code)
	}
	want := "relevo: binding_not_found: no binding for /tmp/x\n  next: relevo bind\n"
	if buf.String() != want {
		t.Errorf("report wrote %q, want %q", buf.String(), want)
	}

	buf.Reset()
	if code := report(&buf, fail(codeInternal, "boom"), false); code != 1 {
		t.Errorf("report exit = %d, want 1", code)
	}
	if got := buf.String(); got != "relevo: internal: boom\n" {
		t.Errorf("report wrote %q, want no next line", got)
	}
}

// TestReportJSONErrorEnvelope pins the machine shape: one JSON object with the
// same three fields, and the exit from the catalog.
func TestReportJSONErrorEnvelope(t *testing.T) {
	var buf bytes.Buffer
	code := report(&buf, failNext(codeUsage, "relevo help", "flag provided but not defined: -bogus"), true)
	if code != 2 {
		t.Errorf("report exit = %d, want 2", code)
	}
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Errorf("envelope spans %d lines, want 1: %q", n, buf.String())
	}

	var got errorEnvelope
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal envelope %q: %v", buf.String(), err)
	}
	want := errorEnvelope{Error: errorDocument{
		Code:    codeUsage,
		Message: "flag provided but not defined: -bogus",
		Next:    "relevo help",
	}}
	if got != want {
		t.Errorf("envelope = %+v, want %+v", got, want)
	}
}

// requireCLIError asserts err is a coded failure, that its code is wantCode,
// and -- when next is non-empty -- that it carries that hint. The returned
// value lets a caller check the message text.
func requireCLIError(t *testing.T, err error, wantCode errorCode, next string) *cliError {
	t.Helper()

	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v (%T) is not a cliError", err, err)
	}
	if ce.code != wantCode {
		t.Errorf("code = %q, want %q", ce.code, wantCode)
	}
	if next != "" && ce.next != next {
		t.Errorf("next = %q, want %q", ce.next, next)
	}
	return ce
}

// TestJSONRequestedScansTokens pins the scan as an exact token match.
func TestJSONRequestedScansTokens(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"status"}, false},
		{[]string{"status", "--json"}, true},
		{[]string{"status", "--json=true"}, false},
		{[]string{"status", "-json"}, false},
		{[]string{"--json", "extra"}, true},
	}
	for _, c := range cases {
		if got := jsonRequested(c.args); got != c.want {
			t.Errorf("jsonRequested(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestFailWithUnlistedCodeIsInternal pins that a typo in a call site is
// contained: the error carries internal and its exit instead of panicking or
// emitting a code the catalog does not declare.
func TestFailWithUnlistedCodeIsInternal(t *testing.T) {
	err := fail(errorCode("not_in_the_catalog"), "boom %d", 1)

	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("fail returned %T, want *cliError", err)
	}
	if ce.code != codeInternal {
		t.Errorf("code = %q, want %q", ce.code, codeInternal)
	}
	if ce.message != "boom 1" {
		t.Errorf("message = %q, want %q", ce.message, "boom 1")
	}

	var ec exitCodeErr
	if !errors.As(err, &ec) || ec.code != 1 {
		t.Errorf("exit = %v, want 1", err)
	}
}
