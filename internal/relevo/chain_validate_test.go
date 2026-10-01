package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestChainStartRefusesActorWithoutDeclaredOutputs(t *testing.T) {
	t.Parallel()

	t.Run("planner without plan artifact", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name:         "shop",
			Plans:        []string{writePlan(t, "step 1")},
			Feature:      "auth",
			MasterMindID: testMasterMindName,
			PlannerActor: "researcher",
		})
		if err == nil {
			t.Fatal("expected refusal for planner without plan artifact, got nil")
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want ErrRefused", err)
		}
		if !strings.Contains(err.Error(), "correct.plan") && !strings.Contains(err.Error(), "rule 4") {
			t.Errorf("expected error to name missing plan artifact or rule 4, got %v", err)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("security without findings count", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		sec := true
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name:          "shop",
			Plans:         []string{writePlan(t, "step 1")},
			Feature:       "auth",
			MasterMindID:  testMasterMindName,
			SecurityActor: "researcher",
			Security:      &sec,
		})
		if err == nil {
			t.Fatal("expected refusal for security without findings count, got nil")
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want ErrRefused", err)
		}
		if !strings.Contains(err.Error(), "findings") && !strings.Contains(err.Error(), "rule 3") {
			t.Errorf("expected error to name missing findings outcome or rule 3, got %v", err)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("resume with planner without plan artifact", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		stoppedChain(t, rt, ChainOptions{})
		_, err := ChainResume(context.Background(), rt, ResumeOptions{
			Name:         "shop",
			PlannerActor: "researcher",
		})
		if err == nil {
			t.Fatal("expected refusal for resume with planner without plan artifact, got nil")
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want ErrRefused", err)
		}
	})
}
