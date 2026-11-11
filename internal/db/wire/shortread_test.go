package wire

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsShortReadWAL(t *testing.T) {
	ioWAL := fmt.Errorf("%w: I/O error (pwrite): WAL frame sync failed", errCodeStub{code: 10})
	ioPlain := fmt.Errorf("%w: I/O error (pwrite): quota exceeded", errCodeStub{code: 10})
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "incident text",
			err:  errors.New("turso: error: I/O error: short read on WAL frame at offset 78312: expected 4096 bytes, got 0"),
			want: true,
		},
		{
			name: "wrapped incident text",
			err:  fmt.Errorf("db: record get: %w", errors.New("short read on WAL frame at offset 0: expected 4096 bytes, got 0")),
			want: true,
		},
		{
			name: "rebuilt wire error carrying the text",
			err:  NewError(1, 10, 10, "I/O error: short read on WAL frame at offset 8: expected 4096 bytes, got 0"),
			want: true,
		},
		{
			name: "code 10 WAL I/O without the frame text",
			err:  ioWAL,
			want: true,
		},
		{
			name: "plain busy",
			err:  NewError(1, 5, 5, "busy"),
			want: false,
		},
		{
			name: "code 10 I/O with no WAL mention",
			err:  ioPlain,
			want: false,
		},
		{
			name: "constraint",
			err:  NewError(1, 19, 19, "constraint failed"),
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("something went wrong"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShortReadWAL(tc.err); got != tc.want {
				t.Errorf("IsShortReadWAL(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// errCodeStub is a code-carrying error for the classifier test, standing in
// for either engine's own error shape.
type errCodeStub struct {
	code int
	msg  string
}

func (e errCodeStub) Error() string { return e.msg }

func (e errCodeStub) Code() int { return e.code }
