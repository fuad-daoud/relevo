//go:build unix

package owner

import (
	"database/sql"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// rowStream reads one query's rows, one row ahead, so the sender knows whether
// the batch that just filled is the last one.
type rowStream struct {
	rows        *sql.Rows
	cols        []string
	pending     []any
	havePending bool
	// ceiling is the largest value this stream may carry, or 0 for no bound.
	// It is set from the connection's ad-hoc marker, so a normal verb's own
	// blobs still flow.
	ceiling int
}

// next fills a batch up to budget and reports whether another row follows.
func (s *rowStream) next(budget int) ([]byte, int, bool, error) {
	batch := &wire.Builder{}
	n := 0
	for batch.Len() < budget {
		vals, ok, err := s.take()
		if err != nil {
			return nil, 0, false, err
		}
		if !ok {
			break
		}
		for _, v := range vals {
			if err := batch.Add(v); err != nil {
				return nil, 0, false, err
			}
		}
		n++
	}
	more, err := s.peek()
	if err != nil {
		return nil, 0, false, err
	}
	return batch.Bytes(), n, more, nil
}

func (s *rowStream) take() ([]any, bool, error) {
	if s.havePending {
		s.havePending = false
		return s.pending, true, nil
	}
	return s.read()
}

func (s *rowStream) peek() (bool, error) {
	if s.havePending {
		return true, nil
	}
	vals, ok, err := s.read()
	if err != nil || !ok {
		return false, err
	}
	s.pending, s.havePending = vals, true
	return true, nil
}

func (s *rowStream) read() ([]any, bool, error) {
	if !s.rows.Next() {
		return nil, false, s.rows.Err()
	}
	vals := make([]any, len(s.cols))
	ptrs := make([]any, len(s.cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := s.rows.Scan(ptrs...); err != nil {
		return nil, false, err
	}
	// The check runs the moment the engine yields the row, before the caller
	// copies the value into a batch or peeks the next row.
	if err := s.checkCeiling(vals); err != nil {
		return nil, false, err
	}
	return vals, true, nil
}
