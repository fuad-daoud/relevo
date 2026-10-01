//go:build unix

package owner

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// adHocValueCeiling is the largest single value the owner carries to an ad-hoc
// connection. The engine materialises a whole value before relevo can see it,
// so an ad-hoc read of one huge value would grow the daemon's heap by it (up to
// SQLite's 1 GB per-value limit) however small the client's --max-bytes is; the
// ceiling refuses such a value before the owner copies it into a batch or peeks
// the next row. It is a var so a test can shrink it rather than allocate 64 MiB,
// and defaults to the protocol constant db query caps --max-bytes at.
var adHocValueCeiling = wire.AdHocReadCeiling

// errAdHocValueTooLarge names a value the owner refused to carry to an ad-hoc
// connection because it is over adHocValueCeiling.
var errAdHocValueTooLarge = errors.New("ad-hoc read: value over the value ceiling")

// adHocCeiling is the single-value ceiling in force for this connection: the
// ad-hoc read path's cap for a client-marked connection, and none for every
// other verb, whose own blobs may be large.
func (c *conn) adHocCeiling() int {
	if c.adHoc {
		return adHocValueCeiling
	}
	return 0
}

// valueBytes is a column value's payload size, which is what the owner would
// copy into a batch; the other storage classes carry a fixed token, not bytes.
func valueBytes(v any) int {
	switch t := v.(type) {
	case []byte:
		return len(t)
	case string:
		return len(t)
	}
	return 0
}

// checkCeiling refuses a row whose value is over the stream's ceiling, checked
// the moment the engine yields the row so the caller never copies it into a
// batch or peeks the next row first.
func (s *rowStream) checkCeiling(vals []any) error {
	if s.ceiling <= 0 {
		return nil
	}
	for _, v := range vals {
		if n := valueBytes(v); n > s.ceiling {
			return fmt.Errorf("%w: %d bytes over %d", errAdHocValueTooLarge, n, s.ceiling)
		}
	}
	return nil
}
