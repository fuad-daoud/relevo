package main

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// chainDryRun is `relevo chain --dry-run`: it resolves and validates the
// workflow and prints the graph. It starts nothing. A workflow that does not
// validate prints every problem and then refuses.
func chainDryRun(ctx context.Context, rt relevo.Runtime, opts relevo.ChainOptions) error {
	doc, err := relevo.ChainDryRun(ctx, rt, opts)
	if err != nil {
		return writeError(err)
	}
	fmt.Print(relevo.RenderChainDryRun(doc))
	if n := len(doc.Problems); n > 0 {
		return fail(codeRefused, "workflow %s has %d problem(s); nothing was started", doc.Workflow, n)
	}
	return nil
}
