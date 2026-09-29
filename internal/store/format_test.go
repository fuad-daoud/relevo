package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/jsonshape"
)

// update rewrites the golden file, but only when BindingFormat was bumped: see
// goldenDecision.
var update = flag.Bool("update", false, "rewrite testdata/binding-shape.golden when BindingFormat was bumped")

const bindingGoldenPath = "testdata/binding-shape.golden"

const (
	bindingShapeMsg   = "store.Binding's JSON shape changed: bump store.BindingFormat, then run go test ./internal/store -run TestBindingShapeMatchesFormat -update"
	bindingRefusalMsg = "bump store.BindingFormat first; an older relevo would erase the new fields"
	bindingStaleMsg   = "store.Binding's golden format line is stale; run go test ./internal/store -run TestBindingShapeMatchesFormat -update"
)

// goldenFile is the parsed golden: its format line and its key paths.
type goldenFile struct {
	format int
	keys   []string
}

func parseGolden(raw []byte) (goldenFile, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return goldenFile{}, errors.New("golden file is empty")
	}
	rest, ok := strings.CutPrefix(lines[0], "format ")
	if !ok {
		return goldenFile{}, fmt.Errorf("golden first line = %q, want \"format <N>\"", lines[0])
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return goldenFile{}, fmt.Errorf("golden format line: %w", err)
	}
	return goldenFile{format: n, keys: lines[1:]}, nil
}

func marshalGolden(format int, keys []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "format %d\n", format)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// goldenDecision is the -update rule as a pure function. A rewrite needs
// BindingFormat to be greater than the golden's format line, so a mismatch
// with no bump fails rather than accepting a shape an older relevo would
// erase.
func goldenDecision(goldenFormat, codeFormat int, same bool) (write bool, msg string) {
	if same && goldenFormat == codeFormat {
		return false, ""
	}
	if codeFormat > goldenFormat {
		return true, ""
	}
	return false, bindingRefusalMsg
}

// checkBindingShape compares keys against the golden and returns the message
// to fail with, or "" when all is well.
func checkBindingShape(g goldenFile, codeFormat int, keys []string, update bool) string {
	same := slices.Equal(g.keys, keys)
	if update {
		write, msg := goldenDecision(g.format, codeFormat, same)
		if msg != "" {
			return msg
		}
		if write {
			if err := os.WriteFile(bindingGoldenPath, marshalGolden(codeFormat, keys), 0o644); err != nil {
				return fmt.Sprintf("write %s: %v", bindingGoldenPath, err)
			}
			return ""
		}
	}
	if !same {
		return bindingShapeMsg
	}
	if g.format != codeFormat {
		if g.format > codeFormat {
			return bindingRefusalMsg
		}
		return bindingStaleMsg
	}
	return ""
}

func readBindingGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile(bindingGoldenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return goldenFile{}
		}
		t.Fatalf("read %s: %v", bindingGoldenPath, err)
	}
	g, err := parseGolden(raw)
	if err != nil {
		t.Fatalf("%s: %v", bindingGoldenPath, err)
	}
	return g
}

// TestBindingShapeMatchesFormat pins that a new field is a new key path, which
// fails until BindingFormat is bumped and the golden regenerated.
func TestBindingShapeMatchesFormat(t *testing.T) {
	g := readBindingGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(Binding{}))

	if msg := checkBindingShape(g, BindingFormat, keys, *update); msg != "" {
		t.Fatal(msg)
	}
	if got := readBindingGolden(t); got.format != BindingFormat {
		t.Errorf("golden format = %d, want BindingFormat %d", got.format, BindingFormat)
	}
}

// TestBindingShapeFailurePath exercises the golden failure path without
// mutating the real type.
func TestBindingShapeFailurePath(t *testing.T) {
	type bindingWithANewField struct {
		Binding
		BrandNew string `json:"brand_new"`
	}
	g := readBindingGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(bindingWithANewField{}))

	if slices.Equal(g.keys, keys) {
		t.Fatal("an extra field must change the key paths")
	}
	if msg := checkBindingShape(g, BindingFormat, keys, false); msg != bindingShapeMsg {
		t.Errorf("mismatch message = %q, want %q", msg, bindingShapeMsg)
	}
	if msg := checkBindingShape(g, BindingFormat, keys, true); msg != bindingRefusalMsg {
		t.Errorf("-update without a bump = %q, want %q", msg, bindingRefusalMsg)
	}
}

func TestGoldenDecision(t *testing.T) {
	cases := []struct {
		name                     string
		goldenFormat, codeFormat int
		same                     bool
		write                    bool
		msg                      string
	}{
		{"same keys, same format", 1, 1, true, false, ""},
		{"keys changed, format bumped", 1, 2, false, true, ""},
		{"keys changed, no bump", 1, 1, false, false, bindingRefusalMsg},
		{"keys changed, code older", 2, 1, false, false, bindingRefusalMsg},
		{"keys match, format bumped", 1, 2, true, true, ""},
		{"keys match, golden newer", 2, 1, true, false, bindingRefusalMsg},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			write, msg := goldenDecision(c.goldenFormat, c.codeFormat, c.same)
			if write != c.write || msg != c.msg {
				t.Errorf("goldenDecision(%d, %d, %v) = (%v, %q), want (%v, %q)",
					c.goldenFormat, c.codeFormat, c.same, write, msg, c.write, c.msg)
			}
		})
	}
}

func TestStoredFormat(t *testing.T) {
	if got := storedFormat(1); got != 0 {
		t.Errorf("storedFormat(1) = %d, want 0: format 1 is absent on disk", got)
	}
	if got := storedFormat(2); got != 2 {
		t.Errorf("storedFormat(2) = %d, want 2", got)
	}
}

// TestSaveWritesCurrentFormat pins that every record carries the current
// format, so an older relevo refuses it rather than erasing the shape key.
func TestSaveWritesCurrentFormat(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/home/dev/projects/webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := bindingRecordJSON(t, s, "webshop")
	if !bytes.Contains(raw, []byte(fmt.Sprintf(`"format":%d`, BindingFormat))) {
		t.Errorf("a binding must carry format %d:\n%s", BindingFormat, raw)
	}
}

// TestBindingShapeDefaultsToWriter pins the shape rule: a record with no shape
// key (format 8 and earlier) decodes as a writer, because a reader could not be
// bound then, and a record written now names its shape at the current format.
func TestBindingShapeDefaultsToWriter(t *testing.T) {
	var old Binding
	if err := json.Unmarshal([]byte(`{"format":8,"name":"old","cwd":"/repo","actor":"builder"}`), &old); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if old.Shape != ShapeWriter {
		t.Errorf("format-8 record Shape = %q, want %q", old.Shape, ShapeWriter)
	}

	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/home/dev/projects/webshop")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := bindingRecordJSON(t, s, "webshop")
	if !bytes.Contains(raw, []byte(`"shape":"writer"`)) {
		t.Errorf("a binding must carry shape \"writer\":\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(fmt.Sprintf(`"format":%d`, BindingFormat))) {
		t.Errorf("a binding must carry format %d:\n%s", BindingFormat, raw)
	}
}

// TestSaveRefusesANewerFormat pins that a binding written by a newer relevo is
// refused, both by Save and by the read.
func TestSaveRefusesANewerFormat(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/projects/webshop")
	b.Format = BindingFormat + 1

	err := s.Save(b)
	var newer *ErrNewerFormat
	if !errors.As(err, &newer) {
		t.Fatalf("Save of a newer format = %v, want *ErrNewerFormat", err)
	}
	if !errors.Is(err, ErrNewerFormatSentinel) {
		t.Errorf("errors.Is(%v, ErrNewerFormatSentinel) = false, want true", err)
	}
	if newer.Kind != "binding" || newer.Name != b.Name || newer.Have != BindingFormat+1 || newer.Know != BindingFormat {
		t.Errorf("ErrNewerFormat = %+v", newer)
	}
	wantText := fmt.Sprintf(`binding "webshop" was written by a newer relevo (format %d; this relevo knows %d): upgrade relevo; a mastermind session reconnects relevo mcp with /mcp`, BindingFormat+1, BindingFormat)
	if err.Error() != wantText {
		t.Errorf("ErrNewerFormat text = %q, want %q", err.Error(), wantText)
	}

	// A record row a newer relevo wrote is refused.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(fmt.Sprintf(`"format":%d`, BindingFormat+1))) {
		t.Fatalf("the fixture must carry format %d, got:\n%s", BindingFormat+1, raw)
	}
	putRecordJSON(t, s, b.Name, string(raw))
	if _, err := s.Load(b.Name); !errors.As(err, &newer) {
		t.Fatalf("Load of a newer-format record = %v, want *ErrNewerFormat", err)
	}
}

func TestKnownState(t *testing.T) {
	for _, s := range []State{StateActive, StateNeedsYou, StateBroken, StateDone, StatePaused} {
		if !KnownState(s) {
			t.Errorf("KnownState(%q) = false, want true", s)
		}
	}
	for _, s := range []State{"", "frozen", "held", "orphaned"} {
		if KnownState(s) {
			t.Errorf("KnownState(%q) = true, want false", s)
		}
	}
}
