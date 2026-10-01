package roles

import (
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

func TestRegistryActorInfoShape(t *testing.T) {
	t.Parallel()

	reg, err := Build(nil, &candidate.Set{}, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Builder is ShapeWriter, outputs empty
	bInfo, ok := reg.ActorInfo("builder")
	if !ok {
		t.Fatal("ActorInfo(builder) not found")
	}
	if bInfo.Shape != workflow.ShapeWriter {
		t.Errorf("builder shape = %v, want %v", bInfo.Shape, workflow.ShapeWriter)
	}
	if len(bInfo.Outputs) != 0 {
		t.Errorf("builder outputs = %v, want empty", bInfo.Outputs)
	}

	// Reviewer is ShapeReader, outputs has verdict & findings
	rInfo, ok := reg.ActorInfo("reviewer")
	if !ok {
		t.Fatal("ActorInfo(reviewer) not found")
	}
	if rInfo.Shape != workflow.ShapeReader {
		t.Errorf("reviewer shape = %v, want %v", rInfo.Shape, workflow.ShapeReader)
	}
	wantReviewerOutputs := workflow.Outputs{
		"verdict":  {Kind: workflow.OutputOneOf, Values: []string{"pass", "changes"}},
		"findings": {Kind: workflow.OutputArtifact},
	}
	if !reflect.DeepEqual(rInfo.Outputs, wantReviewerOutputs) {
		t.Errorf("reviewer outputs = %+v, want %+v", rInfo.Outputs, wantReviewerOutputs)
	}

	// Unknown actor returns false
	if _, ok := reg.ActorInfo("unknown"); ok {
		t.Error("ActorInfo(unknown) returned true, want false")
	}

	// WorkflowActors maps all actors
	all := reg.WorkflowActors()
	if len(all) < 3 {
		t.Errorf("WorkflowActors() len = %d, want at least 3", len(all))
	}
	if all["builder"].Shape != workflow.ShapeWriter {
		t.Errorf("all[builder].Shape = %v, want ShapeWriter", all["builder"].Shape)
	}
	if all["reviewer"].Shape != workflow.ShapeReader {
		t.Errorf("all[reviewer].Shape = %v, want ShapeReader", all["reviewer"].Shape)
	}
}
