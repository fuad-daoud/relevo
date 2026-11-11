package wire

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsShortReadWALMatchesTheEngineText pins the classifier the WAL heal
// turns on: the engine's short-frame text matches directly, wrapped as the
// daemon sees it, and rebuilt through a wire Error, while nil and other
// failures do not.
func TestIsShortReadWALMatchesTheEngineText(t *testing.T) {
	engine := errors.New("turso: error: I/O error: short read on WAL frame at offset 32: expected 4096 bytes, got 0")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a short read", nil, false},
		{"engine error matches", engine, true},
		{"wrapped engine error matches", fmt.Errorf("db: version: %w", engine), true},
		{"rebuilt wire error matches", NewError(7, 10, 10, engine.Error()), true},
		{"other engine error does not", errors.New("turso: error: database is locked"), false},
		{"wrapped other error does not", fmt.Errorf("db: version: %w", errors.New("no such table: t")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShortReadWAL(tc.err); got != tc.want {
				t.Errorf("IsShortReadWAL(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
