package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/pathscope"
)

// ChangedFiles lists every path that differs between the trees from and to,
// with each path's status, modes and blob ids, for the scope check (#801).
//
// It runs `git diff --raw -z --no-renames --abbrev=40`: renames are turned
// off, so a rename is reported as a delete plus an add, and the NUL
// delimiters keep a path with spaces or a newline in it intact.
func (c *Client) ChangedFiles(ctx context.Context, dir, from, to string) ([]pathscope.Change, error) {
	out, err := c.run(ctx, dir, nil,
		"diff", "--raw", "-z", "--no-renames", "--abbrev=40", from, to)
	if err != nil {
		return nil, err
	}
	return parseRawDiff(out)
}

// ReadBlob reads one blob's contents by its git object id, as the scope
// check's comment judge needs (#801).
func (c *Client) ReadBlob(ctx context.Context, dir, oid string) ([]byte, error) {
	return c.run(ctx, dir, nil, "cat-file", "blob", oid)
}

// parseRawDiff parses `git diff --raw -z` output: a run of
//
//	:<oldmode> <newmode> <oldsha> <newsha> <status>\0<path>\0
//
// records. It is exported to no one; the Client methods above are the seam.
func parseRawDiff(out []byte) ([]pathscope.Change, error) {
	if len(out) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(out), "\x00")
	var changes []pathscope.Change
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := fields[i], fields[i+1]
		if meta == "" && path == "" {
			break
		}
		if !strings.HasPrefix(meta, ":") {
			return nil, fmt.Errorf("raw diff: bad record %q", meta)
		}
		parts := strings.Fields(meta[1:])
		if len(parts) != 5 || parts[4] == "" {
			return nil, fmt.Errorf("raw diff: bad record %q", meta)
		}
		changes = append(changes, pathscope.Change{
			OldMode: parts[0],
			NewMode: parts[1],
			OldOID:  parts[2],
			NewOID:  parts[3],
			Status:  parts[4][0],
			Path:    path,
		})
	}
	return changes, nil
}
