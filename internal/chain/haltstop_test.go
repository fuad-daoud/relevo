package chain

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/refusal"
)

func TestHaltedStopTextNamesDone(t *testing.T) {
	t.Parallel()

	const want = "chain shop is halted: `relevo done shop` closes it"
	if got := ChainHaltedStopText("shop"); got != want {
		t.Errorf("ChainHaltedStopText = %q, want %q", got, want)
	}
}

func TestHaltedStopCarriesErrChainHalted(t *testing.T) {
	t.Parallel()

	err := HaltedStop("shop")
	if !errors.Is(err, ErrChainHalted) {
		t.Errorf("HaltedStop = %v, want it to carry ErrChainHalted", err)
	}
	const want = "chain shop is halted: `relevo done shop` closes it"
	if err.Error() != want {
		t.Errorf("HaltedStop.Error() = %q, want %q", err.Error(), want)
	}
	var halted *ChainHaltedError
	if !errors.As(err, &halted) {
		t.Fatalf("HaltedStop = %T, want *ChainHaltedError", err)
	}
	if halted.Name != "shop" {
		t.Errorf("HaltedStop Name = %q, want %q", halted.Name, "shop")
	}
}

func TestChainHaltedErrorUnwrapsToErrChainHalted(t *testing.T) {
	t.Parallel()

	err := &ChainHaltedError{Name: "shop"}
	if !errors.Is(err, ErrChainHalted) {
		t.Errorf("ChainHaltedError does not carry ErrChainHalted")
	}
	if got := err.Unwrap(); !errors.Is(got, ErrChainHalted) {
		t.Errorf("Unwrap() = %v, want ErrChainHalted", got)
	}
}

func TestRefusedBuildsARefusal(t *testing.T) {
	t.Parallel()

	err := Refused("chain %s is still halted: %s", "shop", "no budget left")
	if !errors.Is(err, refusal.ErrRefused) {
		t.Errorf("Refused = %v, want it in the refusal class", err)
	}
	for _, want := range []string{"shop", "no budget left"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Refused.Error() = %q, want it to contain %q", err.Error(), want)
		}
	}
}
