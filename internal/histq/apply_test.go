package histq

import "testing"

func TestApplyWordMatchesAnyOfThree(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		word string
		want int
	}{
		{"api", 6},
		{"web", 5},
		{"checkout", 5},
		{"search", 5},
		{"SEARCH", 5},
		{"agy", 0}, // the builder column is deliberately not one of the three
	}
	for _, c := range cases {
		q := Query{Words: []string{c.word}}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(word %q) kept %d rows, want %d", c.word, got, c.want)
		}
	}
}

// A nil cost, commits or duration fails every comparison; a nil token column
// counts as zero.
func TestApplyNumericConds(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		query string
		want  int
	}{
		{"cost>0", 9},
		{"cost>=2", 4},
		{"tokens>=2000", 4},
		{"tokens=1000", 2},
		{"commits>=1", 7},
		{"commits<3", 6},
		{"duration<=60", 6},
		{"duration>60", 3},
		{"round>2", 4},
		{"round=3", 3},
		{"cost>=1 commits>=2", 4},
	}
	for _, c := range cases {
		q, err := ParseAt(c.query, parseNow)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", c.query, err)
		}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(%q) kept %d rows, want %d", c.query, got, c.want)
		}
	}
}

func TestApplyEnumConds(t *testing.T) {
	rows := fixtureRows()

	cases := []struct {
		query string
		want  int
	}{
		{"report:done", 4},
		{"report:deferred", 1},
		{"gate:pass", 3},
		{"gate:timeout", 1},
		{"basis:unknown", 2},
		{"basis:measured", 5},
		{"server:contabo", 5},
		{"server:local", 5},
		{"mode:remote", 3},
		{"mode:pane", 4},
		{"mode:headless", 3},
		{"report:done mode:pane", 3},
		{"gate:pass mode:remote", 0},
	}
	for _, c := range cases {
		q, err := ParseAt(c.query, parseNow)
		if err != nil {
			t.Fatalf("ParseAt(%q): %v", c.query, err)
		}
		if got := len(q.Apply(rows)); got != c.want {
			t.Errorf("Apply(%q) kept %d rows, want %d", c.query, got, c.want)
		}
	}
}
