package jsonshape

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

type marshalLeaf struct {
	N int
}

func (marshalLeaf) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type fixtureInner struct {
	NoTag   string
	Tagged  int    `json:"tagged,omitempty"`
	Skipped string `json:"-"`
}

// fixture exercises every rule Keys documents: a tagless field, a "-" field,
// an omitempty option, an embedded struct, a pointer, a slice of structs, a
// map of structs, a time.Time and a MarshalJSON leaf.
type fixture struct {
	fixtureInner
	Ptr     *fixtureInner           `json:"ptr"`
	Items   []fixtureInner          `json:"items"`
	Lookup  map[string]fixtureInner `json:"lookup"`
	When    time.Time               `json:"when"`
	Leaf    marshalLeaf             `json:"leaf"`
	Renamed string                  `json:"renamed_tag,omitempty"`
	Plain   string
}

func TestKeys(t *testing.T) {
	got := Keys(reflect.TypeOf(fixture{}))
	want := []string{
		"NoTag", // tagless: the Go field name, from the embedded struct
		"Plain", // tagless: the Go field name
		"items[].NoTag",
		"items[].tagged",
		"leaf", // a MarshalJSON type is a leaf
		"lookup{}.NoTag",
		"lookup{}.tagged",
		"ptr.NoTag",
		"ptr.tagged",
		"renamed_tag", // the tag name, not the Go name
		"tagged",
		"when", // time.Time is a leaf
	}
	if !slices.Equal(got, want) {
		t.Errorf("Keys(fixture{}) =\n%q\nwant\n%q", got, want)
	}
}

func TestKeysSkipsTheDashField(t *testing.T) {
	for _, key := range Keys(reflect.TypeOf(fixture{})) {
		if key == "Skipped" || key == "skipped" {
			t.Errorf(`a json:"-" field must contribute no key, got %q`, key)
		}
	}
}
