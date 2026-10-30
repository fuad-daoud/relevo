package remote

import (
	"errors"
	"fmt"
	"testing"
)

type statusErr int

func (e statusErr) Error() string   { return fmt.Sprintf("status %d", int(e)) }
func (e statusErr) HTTPStatus() int { return int(e) }

func TestIs404MatchesOnlyANotFoundStatus(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{statusErr(404), true},
		{fmt.Errorf("fetch: %w", statusErr(404)), true},
		{statusErr(405), false},
		{statusErr(500), false},
		{errors.New("404"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := Is404(c.err); got != c.want {
			t.Errorf("Is404(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
