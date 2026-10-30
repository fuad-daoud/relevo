package spawn

import (
	"reflect"
	"testing"
)

// TestParseCPUList pins the cpu-list grammar: sorted, de-duplicated cores;
// a backwards range or a core above 1023 is an error.
func TestParseCPUList(t *testing.T) {
	good := []struct {
		in   string
		want []int
	}{
		{"0", []int{0}},
		{"0-2", []int{0, 1, 2}},
		{"1,3,5-7", []int{1, 3, 5, 6, 7}},
		{"3,1-3", []int{1, 2, 3}},
	}
	for _, c := range good {
		t.Run("good/"+c.in, func(t *testing.T) {
			got, err := ParseCPUList(c.in)
			if err != nil {
				t.Fatalf("ParseCPUList(%q): %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseCPUList(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}

	for _, in := range []string{"", "0-", "a", "1 ,2", "3-1", "0-1024"} {
		t.Run("bad/"+in, func(t *testing.T) {
			if _, err := ParseCPUList(in); err == nil {
				t.Fatalf("ParseCPUList(%q): got nil error, want one", in)
			}
		})
	}
}
