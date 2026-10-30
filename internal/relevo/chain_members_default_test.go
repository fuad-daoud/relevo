package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
)

// TestConvertedRowAndNewRowNameMembersAlike pins that a converted row and a new
// start agree: chain.MemberNames on a converted row's stored definition returns
// exactly the legacy names its member bindings already carry, so a resume never
// creates a second member for an actor the chain already runs.
func TestConvertedRowAndNewRowNameMembersAlike(t *testing.T) {
	rt, _ := chainRuntime(t)
	c := legacyChainRow("shop", "running", "build", "building", "builder", 1, 0)
	c.SettingsJSON = []byte(`{"MaxCorrections":3,"Security":true}`)
	seedLegacyChain(t, rt, c, legacyChainBindings("shop"))

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}
	def, _ := convertedState(t, rt, "shop")
	got, err := chain.MemberNames("shop", def, rt.RoleRegistry().WorkflowActors())
	if err != nil {
		t.Fatalf("chainMemberNames: %v", err)
	}
	names := memberNames(t, got)
	want := map[string]string{
		"shop":      "builder",
		"shop-rev":  "reviewer",
		"shop-plan": "lite-planner",
		"shop-sec":  "security",
	}
	for name, actor := range want {
		if names[name] != actor {
			t.Errorf("member %q = %q, want %q", name, names[name], actor)
		}
	}
	if len(names) != len(want) {
		t.Errorf("members = %v, want %v", names, want)
	}
}

// TestResumeAddsSecurityMemberOnConvertedRowAsSec pins that a converted row
// with the scan off gains exactly one member, named with the legacy "-sec"
// suffix, when a resume turns the scan on.
func TestResumeAddsSecurityMemberOnConvertedRowAsSec(t *testing.T) {
	rt, _ := chainRuntime(t)
	c := legacyChainRow("shop", "halted", "build", "building", "builder", 1, 0)
	c.Security = ""
	seedLegacyChain(t, rt, c, legacyChainBindings("shop")[:3])

	if err := ConvertLegacyChains(rt); err != nil {
		t.Fatalf("ConvertLegacyChains: %v", err)
	}
	before, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers before: %v", err)
	}
	if _, err := rt.Store.Load("shop-sec"); err == nil {
		t.Fatal("test premise: the security member exists before the scan is on")
	}

	if _, err := ChainResume(context.Background(), rt, ResumeOptions{Name: "shop", Params: map[string]string{"scan": "true"}}); err != nil {
		t.Fatalf("ChainResume --param scan=true: %v", err)
	}

	after, err := rt.Store.ChainMembers("shop")
	if err != nil {
		t.Fatalf("ChainMembers after: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("members after the resume = %d, want exactly one more than %d", len(after), len(before))
	}
	found := false
	for _, m := range after {
		if m.Binding == "shop-sec" {
			found = true
		}
	}
	if !found {
		t.Errorf("chain members = %+v, want shop-sec among them", after)
	}
}
