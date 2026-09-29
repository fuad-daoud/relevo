package db

import (
	"database/sql"
	"time"
)

// rowScanner is the Scan half of *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// collectRows scans every row with scan, then closes the set. A close error is
// reported only when the read itself succeeded.
func collectRows[T any](rows *sql.Rows, scan func(rowScanner) (T, error)) (out []T, err error) {
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	for rows.Next() {
		v, serr := scan(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ptrIfValid[T any](v sql.Null[T]) *T {
	if !v.Valid {
		return nil
	}
	return &v.V
}

func intPtr(v sql.Null[int64]) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.V)
	return &n
}

func firstValid(vs ...sql.Null[string]) *string {
	for _, v := range vs {
		if v.Valid {
			return &v.V
		}
	}
	return nil
}

func scanString(s rowScanner) (string, error) {
	var v string
	if err := s.Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

func nullTimeFrom(s sql.Null[string]) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.V)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
