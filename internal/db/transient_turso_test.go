//go:build !modernc

package db

import (
	"fmt"
	"testing"

	turso "turso.tech/database/tursogo"
)

func TestIsTransientTurso(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "turso-generic-io-wrapped",
			err:  fmt.Errorf("%w: I/O error (pwrite): quota exceeded", turso.ErrTursoGeneric),
			want: true,
		},
		{
			name: "turso-status-to-error-shape",
			err:  fmt.Errorf("%w: %s", turso.ErrTursoGeneric, "I/O error (pwrite): quota exceeded"),
			want: true,
		},
		{
			name: "turso-nested-query-error",
			err:  fmt.Errorf("db: record get /\"f861\": %w", fmt.Errorf("%w: I/O error (pwrite): quota exceeded", turso.ErrTursoGeneric)),
			want: true,
		},
		{
			name: "turso-generic-syntax",
			err:  fmt.Errorf("%w: syntax error near SELECT", turso.ErrTursoGeneric),
			want: false,
		},
		{
			name: "turso-constraint",
			err:  turso.ErrTursoConstraint,
			want: false,
		},
		{
			name: "turso-busy",
			err:  turso.ErrTursoBusy,
			want: true,
		},
		{
			name: "turso-corrupt",
			err:  turso.ErrTursoCorrupt,
			want: false,
		},
		{
			name: "turso-notadb",
			err:  turso.ErrTursoNotADb,
			want: false,
		},
		{
			name: "turso-misuse",
			err:  turso.ErrTursoMisuse,
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
