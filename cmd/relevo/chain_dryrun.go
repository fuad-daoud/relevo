package main

import (
	"context"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// chainDryRun is `relevo chain --dry-run`: it resolves and validates the
// workflow and prints the graph, each step's actor and placement, every
// reference and the members it would create. It starts nothing, so no worktree,
// binding or chain row is made. A workflow that does not validate prints every
// problem and then refuses, so the exit code still tells a script it is not
// runnable.
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
