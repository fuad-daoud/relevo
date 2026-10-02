package db

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

func TestIsTransient(t *testing.T) {
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
			name: "err-busy",
			err:  ErrBusy,
			want: true,
		},
		{
			name: "wire-busy-code-5",
			err:  wire.NewError(1, 5, 5, "busy"),
			want: true,
		},
		{
			name: "wire-io-code-10",
			err:  wire.NewError(1, 10, 10, "turso: error: I/O error (pwrite): quota exceeded"),
			want: true,
		},
		{
			name: "err-not-found",
			err:  ErrNotFound,
			want: false,
		},
		{
			name: "err-newer-schema",
			err:  ErrNewerSchema,
			want: false,
		},
		{
			name: "wire-constraint-code-19",
			err:  wire.NewError(1, 19, 19, "constraint failed"),
			want: false,
		},
		{
			name: "plain-generic",
			err:  errors.New("something went wrong"),
			want: false,
		},
		{
			name: "refusal",
			err:  &wire.Refusal{Code: wire.RefuseRestarting},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransient(tc.err); got != tc.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
