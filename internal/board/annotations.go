package board

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// annotationsFile is the one file name an HTML board's notes carry. It sits
// beside board.html rather than inside it, so a board's bytes on disk never
// change when someone comments on it, and a note survives a board being
// re-saved from a different tool.
const annotationsFile = "annotations.json"

// The annotation caps. The text cap is the same order as a comment's payload
// limit and the selector cap is the longest CSS path this code will ever build
// or read back; both are refusals, not truncations, so a stored note is never
// quieter than what was written.
const (
	maxAnnotationText     = 8192
	maxAnnotationSelector = 1024
	// annotationIDHex is how many hex characters follow the "a" prefix. Twelve
	// is 48 bits: collisions are not the concern, an id being guessable from
	// the file's own contents is, because an id is what a click focuses.
	annotationIDHex = 12
)

// Annotation is one note on an HTML board. Selector is a CSS selector for the
// element the note is about, and empty means the note is about the board
// itself. X and Y are fractions of the anchored element's box rather than page
// pixels, so a pin lands in the same relative place after a resize.
type Annotation struct {
	ID       string  `json:"id"`
	Selector string  `json:"selector"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Text     string  `json:"text"`
	By       string  `json:"by"`
	At       string  `json:"at"`
}

// AnnotationRequest is what one append asks for. By is filled in by the
// caller: the CLI from --by or the environment, the server always human, so a
// page post can never author a note in someone else's name.
type AnnotationRequest struct {
	Selector string
	X, Y     float64
	Text     string
	By       string
	At       time.Time
}

// AnnotationsPath returns the annotations file beside the board at boardPath.
func AnnotationsPath(boardPath string) string {
	return filepath.Join(filepath.Dir(boardPath), annotationsFile)
}

// ReadAnnotations returns every note beside the board at boardPath, in file
// order, with the file's etag. A missing file is no notes and an empty etag: a
// board nobody has commented on yet is a board with nothing to show, not an
// error. A symlink is refused: the annotations file is written through an
// atomic rename, so a symlinked one is either a link out of the board's
// directory or nothing this code should follow.
func ReadAnnotations(boardPath string) ([]Annotation, string, error) {
	path := AnnotationsPath(boardPath)
	data, etag, err := readAnnotationsFile(path)
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		return []Annotation{}, "", nil
	}
	entries, err := parseAnnotations(path, data)
	if err != nil {
		return nil, "", err
	}
	return entries, etag, nil
}

// AppendAnnotation adds one note beside the board at boardPath and returns it
// with the file's new etag. A nil ifMatch is unconditional, which is what the
// CLI wants: it reads the etag and appends against it in one step. A non-nil
// ifMatch is the server's compare-and-swap: a value that is not the current
// etag is ErrConflict and nothing is written.
//
// The append only ever adds bytes: the file is re-read, checked against
// ifMatch, spliced and written through the atomic rename, so no existing entry
// is rewritten and board.html is never touched.
func AppendAnnotation(boardPath string, req AnnotationRequest, ifMatch *string) (Annotation, string, error) {
	if _, err := os.Stat(boardPath); err != nil {
		if os.IsNotExist(err) {
			return Annotation{}, "", fmt.Errorf("%w: board %s does not exist", ErrNotFound, boardPath)
		}
		return Annotation{}, "", err
	}
	entry, err := newAnnotation(req)
	if err != nil {
		return Annotation{}, "", err
	}

	path := AnnotationsPath(boardPath)
	data, etag, err := readAnnotationsFile(path)
	if err != nil {
		return Annotation{}, "", err
	}
	if data == nil {
		data, etag = []byte("[]"), ""
	}
	existing, err := parseAnnotations(path, data)
	if err != nil {
		return Annotation{}, "", err
	}
	if ifMatch != nil && *ifMatch != etag {
		return Annotation{}, "", fmt.Errorf("%w: %s is at etag %q, not %q", ErrConflict, path, etag, *ifMatch)
	}

	entry.ID, err = uniqueAnnotationID(existing)
	if err != nil {
		return Annotation{}, "", err
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return Annotation{}, "", err
	}
	out, err := spliceAnnotation(data, line)
	if err != nil {
		return Annotation{}, "", fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	if err := writeAtomic(path, out); err != nil {
		return Annotation{}, "", err
	}
	return entry, Etag(out), nil
}

// readAnnotationsFile reads the annotations file at path. A missing file is nil
// data with no error. A symlink is refused rather than followed, and an
// oversized file is refused whole rather than read in part, so a note list is
// never half-loaded.
func readAnnotationsFile(path string) ([]byte, string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("%w: %s is a symlink", ErrInvalid, path)
	}
	if fi.Size() > maxBody {
		return nil, "", fmt.Errorf("%w: %s is %d bytes, over the %d byte cap",
			ErrInvalid, path, fi.Size(), maxBody)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return data, Etag(data), nil
}

// parseAnnotations decodes the file at path. Unknown fields are tolerated so a
// newer writer's extra keys do not break an older reader. A file that is not a
// JSON array, and an entry without an id or an author or with an at that is not
// RFC3339, are refusals naming the path and, for an entry, its index and id --
// the two things a person needs to find the bad line.
func parseAnnotations(path string, data []byte) ([]Annotation, error) {
	if !strings.HasPrefix(strings.TrimLeft(string(data), " \t\r\n"), "[") {
		return nil, fmt.Errorf("%w: %s: not a JSON array of annotations", ErrInvalid, path)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("%w: %s: not a JSON array of annotations", ErrInvalid, path)
	}
	out := make([]Annotation, 0, len(raws))
	for i, raw := range raws {
		a, err := parseAnnotation(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: annotation %d (%s): %w", ErrInvalid, path, i, a.ID, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// parseAnnotation decodes one entry and checks the two fields nothing can do
// without: who wrote it and when.
func parseAnnotation(raw json.RawMessage) (Annotation, error) {
	var a Annotation
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	if a.ID == "" {
		return a, errors.New("no id")
	}
	if strings.TrimSpace(a.By) == "" {
		return a, errors.New("no author")
	}
	if _, err := time.Parse(time.RFC3339, a.At); err != nil {
		return a, fmt.Errorf("at %q is not RFC3339", a.At)
	}
	return a, nil
}

// newAnnotation validates a request and builds the entry, except for its id,
// which depends on what is already in the file.
func newAnnotation(req AnnotationRequest) (Annotation, error) {
	by := strings.TrimSpace(req.By)
	if err := validateBy(by); err != nil {
		return Annotation{}, err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Annotation{}, usagef("annotation: the text must not be empty")
	}
	if len(text) > maxAnnotationText {
		return Annotation{}, usagef("annotation: the text is %d bytes, want at most %d",
			len(text), maxAnnotationText)
	}
	selector := req.Selector
	if len(selector) > maxAnnotationSelector {
		return Annotation{}, usagef("annotation: the selector is %d bytes, want at most %d",
			len(selector), maxAnnotationSelector)
	}
	for _, r := range selector {
		if r < 0x20 || r == 0x7f {
			return Annotation{}, usagef("annotation: the selector contains a control character")
		}
	}
	x, y := req.X, req.Y
	if selector == "" {
		// A board-level note has no element to be a fraction of, so its position
		// is defined to be the origin rather than left carrying a stale pair.
		x, y = 0, 0
	}
	if err := validFraction("x", x); err != nil {
		return Annotation{}, err
	}
	if err := validFraction("y", y); err != nil {
		return Annotation{}, err
	}
	at := req.At
	if at.IsZero() {
		at = time.Now()
	}
	return Annotation{
		Selector: selector,
		X:        x,
		Y:        y,
		Text:     text,
		By:       by,
		At:       at.Format(time.RFC3339),
	}, nil
}

// validFraction is the position rule: a finite number inside the unit square,
// because x and y are fractions of an element's box and a pin outside it would
// have nothing to be a fraction of.
func validFraction(name string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return usagef("annotation: %s is not a finite number", name)
	}
	if v < 0 || v > 1 {
		return usagef("annotation: %s is %v, want a fraction in [0,1]", name, v)
	}
	return nil
}

// uniqueAnnotationID draws an id and returns one no existing entry carries.
func uniqueAnnotationID(existing []Annotation) (string, error) {
	taken := make(map[string]bool, len(existing))
	for _, a := range existing {
		taken[a.ID] = true
	}
	buf := make([]byte, annotationIDHex/2)
	for range 64 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if id := "a" + hex.EncodeToString(buf); !taken[id] {
			return id, nil
		}
	}
	return "", errors.New("annotation: could not draw an id no entry carries")
}

// spliceAnnotation inserts one entry's line before the array's closing bracket,
// leaving every prior byte exactly as it was. Re-marshalling the array would
// lose formatting and any key this reader does not know, so it never happens.
func spliceAnnotation(data, entry []byte) ([]byte, error) {
	core := strings.TrimRight(string(data), " \t\r\n")
	if !strings.HasSuffix(core, "]") {
		return nil, errors.New("not a JSON array of annotations")
	}
	head := strings.TrimRight(core[:len(core)-1], " \t\r\n")
	switch {
	case head == "[":
		return []byte(head + "\n" + string(entry) + "\n]\n"), nil
	case strings.HasSuffix(head, ","):
		return []byte(head + "\n" + string(entry) + "\n]\n"), nil
	default:
		return []byte(head + ",\n" + string(entry) + "\n]\n"), nil
	}
}
