package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainDeletedActorPart names the part a chain member fills and the chain
// holding it. A member outside the chain's own rows reads as no part, so the
// caller keeps the bare halt instead of naming a command for a part it cannot
// prove.
func chainDeletedActorPart(tx *store.Tx, member string) (part, chainName string) {
	c, err := tx.ChainByMember(member)
	if err != nil {
		return "", ""
	}
	return chain.PartOf(c, member), c.Name
}

// chainDeletedActorHalt names the resume that swaps in another actor when a
// member cannot start because its actor is gone: config actors no longer
// defines the actor the chain started with, and the bare spawn failure does
// not say the actor was removed or what runs the chain again. Any other
// start failure, or a member with no part, keeps the input text, so every
// other halt reads exactly as today.
func chainDeletedActorHalt(part, chainName, actor string, err error, fallback string) string {
	if part == "" || chainName == "" || actor == "" {
		return fallback
	}
	if !errors.Is(err, resolve.ErrUnknownRole) {
		return fallback
	}
	return fmt.Sprintf("%s; actor %q was removed after chain %q started, so resume with another: relevo chain --resume --name %s %s",
		fallback, actor, chainName, chainName, chainDeletedActorFlag(part))
}

// chainDeletedActorFlag is the resume spelling that swaps in another actor
// for part. The reviewer, planner and security swaps have their own resume
// flags; every other part is a workflow param, including the builder, whose
// own flag a resume refuses.
func chainDeletedActorFlag(part string) string {
	switch part {
	case chain.MemberReviewer:
		return "--reviewer-actor <other>"
	case chain.MemberPlanner:
		return "--planner-actor <other>"
	case chain.MemberSecurity:
		return "--security-actor <other>"
	default:
		return "--param " + part + "=<other>"
	}
}
