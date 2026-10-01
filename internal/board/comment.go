package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// commentGap is the vertical clearance an unplaced comment keeps below the
// scene's bounds, so an agent's comment never lands on a drawing and
// consecutive comments stack clear of each other.
const commentGap = 40

// humanBy is the author a comment gets when neither --by nor the
// RELEVO_MASTERMIND environment names one.
const humanBy = "human"

// Comment is one comment as `board comments` reports it, in scene order.
type Comment struct {
	ID   string  `json:"id"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Text string  `json:"text"`
	By   string  `json:"by"`
	At   string  `json:"at"`
}

// CommentRequest is what one `board comment` asks for. A nil X/Y places the
// comment below the scene's bounds.
type CommentRequest struct {
	Text  string
	X, Y  *float64
	By    string
	At    time.Time
	Theme *Theme
}

// DefaultBy resolves the comment author: an explicit value when set, else the
// environment's MasterMind, else human.
func DefaultBy(env string) string {
	if env != "" {
		return env
	}
	return humanBy
}

// ReadComments returns every comment in the scene at path, in elements[] order.
// A missing scene is no comments and creates nothing. A malformed marker --
// comment:true on an element that is not text, or without by, or with an at
// that is not RFC3339 -- refuses naming the element id; a file that is not an
// Excalidraw scene refuses naming the path.
func ReadComments(path string) ([]Comment, error) {
	data, _, isNew, err := Load(path)
	if err != nil {
		return nil, err
	}
	if isNew {
		return []Comment{}, nil
	}
	if err := ValidateScene(data); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	return commentsFromScene(data)
}

// AddComment appends one comment element to the scene at path and writes it
// through the atomic rename, leaving every other byte of the scene unchanged.
// A missing scene becomes a fresh one carrying the element. It returns the
// comment it wrote.
func AddComment(path string, req CommentRequest) (Comment, error) {
	by := strings.TrimSpace(req.By)
	if err := validateBy(by); err != nil {
		return Comment{}, err
	}
	if (req.X == nil) != (req.Y == nil) {
		return Comment{}, usagef("comment: --x and --y go together")
	}
	if req.Theme == nil {
		return Comment{}, errors.New("board: AddComment needs a theme")
	}
	at := req.At
	if at.IsZero() {
		at = time.Now()
	}
	atStr := at.Format(time.RFC3339)

	data, _, isNew, err := Load(path)
	if err != nil {
		return Comment{}, err
	}

	var x, y float64
	if req.X != nil {
		x, y = *req.X, *req.Y
	} else {
		x, y, err = commentPlacement(data, isNew)
		if err != nil {
			return Comment{}, err
		}
	}

	el, err := NewTextElement(TextOptions{
		Text:        req.Text,
		X:           x,
		Y:           y,
		FontSize:    20,
		StrokeColor: req.Theme.Comment,
		Updated:     at.UnixMilli(),
		Marker:      &CommentMarker{Comment: true, By: by, At: atStr},
	})
	if err != nil {
		return Comment{}, err
	}
	element, err := json.Marshal(el)
	if err != nil {
		return Comment{}, err
	}

	var out []byte
	if isNew {
		out, err = freshScene(element, req.Theme.BG)
	} else {
		out, err = AppendElement(data, element)
	}
	if err != nil {
		return Comment{}, fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	if err := writeAtomic(path, out); err != nil {
		return Comment{}, err
	}
	return Comment{ID: el.ID, X: x, Y: y, Text: req.Text, By: by, At: atStr}, nil
}

// validateBy is the author rule: a non-empty value after trimming, at most 64
// bytes, and no control characters. A default resolved from a bogus
// environment is validated too, so it refuses rather than lands in the scene.
func validateBy(by string) error {
	if by == "" {
		return usagef("comment: the author must not be empty")
	}
	if len(by) > 64 {
		return usagef("comment: the author is %d bytes, want at most 64", len(by))
	}
	for _, r := range by {
		if r < 0x20 || r == 0x7f {
			return usagef("comment: the author contains a control character")
		}
	}
	return nil
}

// commentsFromScene walks the top-level elements array in order and returns
// each comment it holds. A marker outside elements is never seen, and a marker
// that is not comment:true is not a comment.
func commentsFromScene(data []byte) ([]Comment, error) {
	var doc struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	out := []Comment{}
	for _, raw := range doc.Elements {
		var el struct {
			ID         string  `json:"id"`
			Type       string  `json:"type"`
			X          float64 `json:"x"`
			Y          float64 `json:"y"`
			Text       string  `json:"text"`
			CustomData struct {
				Relevo *CommentMarker `json:"relevo"`
			} `json:"customData"`
		}
		if err := json.Unmarshal(raw, &el); err != nil || el.CustomData.Relevo == nil {
			continue
		}
		m := el.CustomData.Relevo
		if !m.Comment {
			continue
		}
		if el.Type != "text" {
			return nil, fmt.Errorf("%w: comment element %s is type %q, want %q", ErrInvalid, el.ID, el.Type, "text")
		}
		if strings.TrimSpace(m.By) == "" {
			return nil, fmt.Errorf("%w: comment element %s has no author", ErrInvalid, el.ID)
		}
		if _, err := time.Parse(time.RFC3339, m.At); err != nil {
			return nil, fmt.Errorf("%w: comment element %s has an at that is not RFC3339: %q", ErrInvalid, el.ID, m.At)
		}
		out = append(out, Comment{ID: el.ID, X: el.X, Y: el.Y, Text: el.Text, By: m.By, At: m.At})
	}
	return out, nil
}

// commentPlacement is the unplaced-comment rule: x at the scene's left bound
// and y one gap below its bottom bound, both over the non-deleted elements
// with missing numbers read as zero. An empty scene places at the origin, so a
// comment never lands on a drawing.
func commentPlacement(data []byte, isNew bool) (float64, float64, error) {
	if isNew {
		return 0, 0, nil
	}
	var doc struct {
		Elements []struct {
			X         *float64 `json:"x"`
			Y         *float64 `json:"y"`
			Height    *float64 `json:"height"`
			IsDeleted bool     `json:"isDeleted"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	minX, maxY := math.Inf(1), math.Inf(-1)
	found := false
	for _, e := range doc.Elements {
		if e.IsDeleted {
			continue
		}
		found = true
		x, y, h := num(e.X), num(e.Y), num(e.Height)
		if x < minX {
			minX = x
		}
		if y+h > maxY {
			maxY = y + h
		}
	}
	if !found {
		return 0, 0, nil
	}
	return minX, maxY + commentGap, nil
}

// num reads a possibly-missing element number as zero.
func num(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// freshSceneDoc is the scene `comment` creates when none exists: the appState
// background is what the page's isNew path can no longer supply once the file
// is on disk.
type freshSceneDoc struct {
	Type     string            `json:"type"`
	Version  int               `json:"version"`
	Source   string            `json:"source"`
	Elements []json.RawMessage `json:"elements"`
	AppState map[string]string `json:"appState"`
	Files    map[string]any    `json:"files"`
}

// freshScene composes the document above with one element already inside.
func freshScene(element []byte, bg string) ([]byte, error) {
	return json.Marshal(freshSceneDoc{
		Type:     "excalidraw",
		Version:  2,
		Source:   "local",
		Elements: []json.RawMessage{element},
		AppState: map[string]string{"viewBackgroundColor": bg},
		Files:    map[string]any{},
	})
}
