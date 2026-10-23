HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 7 — pin an explicit pick

Deliverable: a binding whose candidate a person named keeps it across a window
edge.

1. Add `BuilderPinned bool` to `store.Binding` (`internal/store/binding.go:225`
   area). Do NOT bump `BindingFormat`: an older relevo that drops the pin never
   re-picks for a window, so losing it is harmless. Add `builder_pinned` to the
   list of fields recordFormat never stamps in `internal/store/format.go`'s
   doc comment, with that reason, and regenerate the shape golden if its test
   requires it (`go test ./internal/store -run TestBindingShapeMatchesFormat -update`).
   This overrides the spec's §1.5.1 format-bump wording.
2. Set it wherever a resolution becomes the binding's candidate:
   `applyBuilder` (`builder_change.go:55-66`, from `res.How`), the bind rebind
   path and the fresh bind (`bind.go:496`, `:736`), `add` (`add.go:440`), the
   served create (`serve/bindings.go:294`, from the request's named token), and
   the chain member creates that resolve their own pick (`chain_start.go:382`,
   `chain_served.go:282`, `chain_server_start.go:515`).
3. Leave the client-side mirrors of a served binding alone
   (`remote_add.go:349`, `remote_send.go:227`, `remote_sync.go:224`,
   `chain_remote.go:299`, `chain_resume_swap.go:41,56`) and say why in the plan
   report: a remote binding is never re-picked locally.
4. Write `TestApplyBuilderPinsAnExplicitPick`. (`TestPinnedBindingDoesNotRepick`
   belongs to plan 6, which runs after this one.)
5. Run `go test ./internal/store/... ./internal/relevo/... -run 'Pinned|Shape'`
   — green, and `BindingFormat` is still 18.

Commit: `store: pin a builder candidate a person named`.
