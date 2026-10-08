package synclog

import "testing"

// TestImportResultMoved pins the predicate a drain loop stops on: a run that
// applied an entry or dropped a batch moved a mark, and one that only held an
// origin or reported a gap moved nothing. The dropped batch is the case that
// makes Batches useless here: a drop moves the mark through past() without
// incrementing Batches, so a loop that tested Batches would stop with the drop
// behind it and never offer the entries after it again.
func TestImportResultMoved(t *testing.T) {
	cases := []struct {
		name string
		res  ImportResult
		want bool
	}{
		{"an applied entry moved a mark", ImportResult{Applied: 1, Batches: 1}, true},
		{"a dropped batch moved a mark with no entry applied", ImportResult{Dropped: []Drop{{Origin: "m2", Seq: 4}}}, true},
		{"a held origin moved nothing", ImportResult{Held: []Hold{{Origin: "m2"}}}, false},
		{"a gap moved nothing", ImportResult{Gaps: []Gap{{Origin: "m2", Want: 4, Got: 9}}}, false},
		{"an empty run moved nothing", ImportResult{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Moved(); got != tc.want {
				t.Errorf("Moved() = %t for %+v, want %t", got, tc.res, tc.want)
			}
		})
	}
}
