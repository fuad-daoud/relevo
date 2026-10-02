package board

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sceneWith writes one scene document carrying elements, then returns its path.
func sceneWith(t *testing.T, elements string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "board.excalidraw")
	body := `{"type":"excalidraw","version":2,"source":"local","elements":` + elements +
		`,"appState":{},"files":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write scene: %v", err)
	}
	return path
}

// commentScene makes a scene whose single element is a comment with id and at.
func commentScene(t *testing.T, id, at string) string {
	t.Helper()
	return sceneWith(t, `[{"id":"`+id+`","type":"text","x":1,"y":2,"text":"t",`+
		`"customData":{"relevo":{"comment":true,"by":"human","at":"`+at+`"}}}]`)
}

func testTheme(t *testing.T) *Theme {
	t.Helper()
	th, err := Lookup("cockpit")
	if err != nil {
		t.Fatalf("Lookup(cockpit): %v", err)
	}
	return th
}

func testAt() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

// TestCommentElementMarker pins the marker `comment` writes: comment, by and
// at under customData.relevo on a type:"text" element, the only key the writer
// adds there, and an unrelated element's extra customData left byte-identical.
func TestCommentElementMarker(t *testing.T) {
	path := sceneWith(t, `[{"id":"keep","type":"text","text":"k",`+
		`"customData":{"relevo":{"comment":false},"mine":{"n":1}}}]`)

	c, err := AddComment(path, CommentRequest{
		Text: "a note", By: "human", At: testAt(), Theme: testTheme(t),
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scene: %v", err)
	}
	var doc struct {
		Elements []struct {
			ID         string          `json:"id"`
			Type       string          `json:"type"`
			Text       string          `json:"text"`
			CustomData json.RawMessage `json:"customData"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal scene: %v", err)
	}
	if len(doc.Elements) != 2 {
		t.Fatalf("scene has %d elements, want 2", len(doc.Elements))
	}
	got := doc.Elements[1]
	if got.ID != c.ID || got.Type != "text" || got.Text != "a note" {
		t.Errorf("appended element = %+v, want id %s, text, a note", got, c.ID)
	}
	var custom map[string]json.RawMessage
	if err := json.Unmarshal(got.CustomData, &custom); err != nil {
		t.Fatalf("unmarshal customData: %v", err)
	}
	if len(custom) != 1 {
		t.Fatalf("customData = %v, want exactly the relevo key", custom)
	}
	var relevo map[string]json.RawMessage
	if err := json.Unmarshal(custom["relevo"], &relevo); err != nil {
		t.Fatalf("unmarshal relevo: %v", err)
	}
	if len(relevo) != 3 {
		t.Fatalf("relevo = %v, want exactly comment, by, at", relevo)
	}
	if string(relevo["comment"]) != "true" || string(relevo["by"]) != `"human"` || string(relevo["at"]) != `"2026-10-01T12:00:00Z"` {
		t.Errorf("relevo = %v, want comment true, by human, at the fixed time", relevo)
	}

	// The writer never rewrites another element's customData.
	if !strings.Contains(string(data), `"mine":{"n":1}`) {
		t.Errorf("the unrelated element's customData was rewritten: %s", data)
	}
}

// TestReadCommentsMarkerScope pins what counts as a comment: the marker must
// be comment:true, and extra keys inside customData or inside relevo are
// ignored.
func TestReadCommentsMarkerScope(t *testing.T) {
	path := sceneWith(t, `[`+
		`{"id":"yes","type":"text","x":1,"y":2,"text":"read me",`+
		`"customData":{"note":"x","relevo":{"comment":true,"by":"human","at":"2026-10-01T12:00:00Z","extra":7}}}`+
		`]`)
	comments, err := ReadComments(path)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	want := []Comment{{ID: "yes", X: 1, Y: 2, Text: "read me", By: "human", At: "2026-10-01T12:00:00Z"}}
	if len(comments) != 1 || comments[0] != want[0] {
		t.Errorf("ReadComments = %+v, want %+v", comments, want)
	}
}

// TestReadCommentsElementOrderAndMarkerScope pins that a marker outside
// elements is not a comment, that comment:false and comment-less markers are
// not comments, and that comments come back in elements[] order even when the
// stored at values disagree.
func TestReadCommentsElementOrderAndMarkerScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.excalidraw")
	body := `{` +
		`"type":"excalidraw","elements":[` +
		`{"id":"c1","type":"text","x":1,"y":1,"text":"first","customData":{"relevo":{"comment":true,"by":"a","at":"2026-01-01T00:00:00Z"}}},` +
		`{"id":"c2","type":"text","x":2,"y":2,"text":"second","customData":{"relevo":{"comment":true,"by":"b","at":"2020-01-01T00:00:00Z"}}},` +
		`{"id":"no-comment","type":"text","text":"x","customData":{"relevo":{"by":"c","at":"2026-01-01T00:00:00Z"}}},` +
		`{"id":"false","type":"text","text":"x","customData":{"relevo":{"comment":false,"by":"d","at":"2026-01-01T00:00:00Z"}}},` +
		`{"id":"outside","type":"text","text":"x","customData":{"comment":true,"by":"e","at":"2026-01-01T00:00:00Z"}}` +
		`],"appState":{"relevo":{"comment":true,"by":"decoy","at":"2026-01-01T00:00:00Z"}},` +
		`"files":{"relevo":{"comment":true,"by":"decoy","at":"2026-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write scene: %v", err)
	}
	comments, err := ReadComments(path)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 2 || comments[0].ID != "c1" || comments[1].ID != "c2" {
		t.Fatalf("ReadComments = %+v, want c1 then c2 in array order", comments)
	}
}

// TestCommentsRefuseMalformedMarker pins the refusal: a comment:true marker
// that is not text, or has no author, or a non-RFC3339 at, refuses naming the
// element id.
func TestCommentsRefuseMalformedMarker(t *testing.T) {
	cases := []struct {
		name string
		el   string
	}{
		{"not text", `{"id":"bad1","type":"rectangle","text":"x","customData":{"relevo":{"comment":true,"by":"a","at":"2026-01-01T00:00:00Z"}}}`},
		{"no author", `{"id":"bad2","type":"text","text":"x","customData":{"relevo":{"comment":true,"at":"2026-01-01T00:00:00Z"}}}`},
		{"empty author", `{"id":"bad3","type":"text","text":"x","customData":{"relevo":{"comment":true,"by":"  ","at":"2026-01-01T00:00:00Z"}}}`},
		{"bad at", `{"id":"bad4","type":"text","text":"x","customData":{"relevo":{"comment":true,"by":"a","at":"yesterday"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sceneWith(t, `[`+tc.el+`]`)
			_, err := ReadComments(path)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ReadComments = %v, want ErrInvalid", err)
			}
			id := tc.el[7:11]
			if !strings.Contains(err.Error(), id) {
				t.Errorf("refusal %q does not name the element id %q", err, id)
			}
		})
	}
}

// TestCommentsRefuseNonScene pins that a file that is not an Excalidraw scene
// refuses naming the path.
func TestCommentsRefuseNonScene(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`[]`,
		`{"type":"other","elements":[]}`,
		`{"type":"excalidraw"}`,
	} {
		path := filepath.Join(t.TempDir(), "board.excalidraw")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write scene: %v", err)
		}
		_, err := ReadComments(path)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("ReadComments(%s) = %v, want ErrInvalid", body, err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("refusal %q does not name the path %q", err, path)
		}
	}
}

// TestReadCommentsMissingScene pins that a missing scene is no comments and
// creates nothing, not even the directory.
func TestReadCommentsMissingScene(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "board.excalidraw")
	comments, err := ReadComments(path)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("ReadComments = %+v, want none", comments)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("ReadComments created the directory: stat error = %v, want not-exist", err)
	}
}

// TestAddCommentCreatesMissingScene pins the fresh scene the first comment
// writes: the exact Excalidraw envelope with the theme background and the one
// element inside.
func TestAddCommentCreatesMissingScene(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "board.excalidraw")
	th := testTheme(t)
	c, err := AddComment(path, CommentRequest{Text: "first", By: "human", At: testAt(), Theme: th})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fresh scene: %v", err)
	}
	prefix := `{"type":"excalidraw","version":2,"source":"local","elements":[`
	suffix := `],"appState":{"viewBackgroundColor":"` + th.BG + `"},"files":{}}`
	if !strings.HasPrefix(string(data), prefix) || !strings.HasSuffix(string(data), suffix) {
		t.Fatalf("fresh scene = %s\nwant prefix %s and suffix %s", data, prefix, suffix)
	}
	comments, err := ReadComments(path)
	if err != nil {
		t.Fatalf("ReadComments: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != c.ID {
		t.Errorf("ReadComments = %+v, want the written comment %s", comments, c.ID)
	}
}

// TestAddCommentPlacement pins the unplaced rule: x at the left bound, y one
// gap below the bottom bound over the non-deleted elements, missing numbers as
// zero, an empty scene at the origin, and consecutive comments stacking.
func TestAddCommentPlacement(t *testing.T) {
	th := testTheme(t)
	path := sceneWith(t, `[`+
		`{"id":"a","type":"rectangle","x":100,"y":50,"width":20,"height":10},`+
		`{"id":"gone","type":"rectangle","x":10,"y":5,"width":4,"height":5,"isDeleted":true},`+
		`{"id":"b","type":"rectangle","x":30,"width":7,"height":3},`+
		`{"id":"c","type":"ellipse","x":50,"y":100,"height":8}`+
		`]`)

	first, err := AddComment(path, CommentRequest{Text: "one", By: "human", At: testAt(), Theme: th})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if first.X != 30 || first.Y != 148 {
		t.Errorf("first placement = (%v, %v), want (30, 148)", first.X, first.Y)
	}
	second, err := AddComment(path, CommentRequest{Text: "two", By: "human", At: testAt(), Theme: th})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if second.X != 30 || second.Y != 212 {
		t.Errorf("second placement = (%v, %v), want (30, 212)", second.X, second.Y)
	}

	for _, tc := range []struct {
		name     string
		elements string
	}{
		{"empty", `[]`},
		{"all deleted", `[{"id":"d","type":"rectangle","x":9,"y":9,"isDeleted":true}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := sceneWith(t, tc.elements)
			c, err := AddComment(p, CommentRequest{Text: "x", By: "human", At: testAt(), Theme: th})
			if err != nil {
				t.Fatalf("AddComment: %v", err)
			}
			if c.X != 0 || c.Y != 0 {
				t.Errorf("placement = (%v, %v), want (0, 0)", c.X, c.Y)
			}
		})
	}
}

// TestAddCommentExplicitPlacement: --x/--y win over the bounds rule.
func TestAddCommentExplicitPlacement(t *testing.T) {
	x, y := 7.5, 3.5
	path := sceneWith(t, `[{"id":"a","type":"rectangle","x":100,"y":100,"width":1,"height":1}]`)
	c, err := AddComment(path, CommentRequest{Text: "here", X: &x, Y: &y, By: "human", At: testAt(), Theme: testTheme(t)})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c.X != 7.5 || c.Y != 3.5 {
		t.Errorf("placement = (%v, %v), want (7.5, 3.5)", c.X, c.Y)
	}
}

// TestByDefaultAndValidation pins the author rule: the default falls back to
// human, and an empty, too-long or control-character author is a usage
// refusal.
func TestByDefaultAndValidation(t *testing.T) {
	if got := DefaultBy(""); got != "human" {
		t.Errorf("DefaultBy(\"\") = %q, want human", got)
	}
	if got := DefaultBy("mm_aaaaaaaaaaaa"); got != "mm_aaaaaaaaaaaa" {
		t.Errorf("DefaultBy(id) = %q, want the id", got)
	}

	path := commentScene(t, "c1", "2026-01-01T00:00:00Z")
	for _, by := range []string{"", "   ", strings.Repeat("a", 65), "a\tb"} {
		_, err := AddComment(path, CommentRequest{Text: "x", By: by, At: testAt(), Theme: testTheme(t)})
		if !errors.Is(err, ErrUsage) {
			t.Errorf("AddComment(by=%q) = %v, want ErrUsage", by, err)
		}
	}
}
