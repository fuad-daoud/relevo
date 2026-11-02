package main

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// TestBuildServeConfigWiresBurst pins the burst section of the server config:
// the policy block passes through, the client key reaches the server so the
// startup gate can read it, and a command builds the provider while no
// command leaves it nil -- with it nil, every later burst step is a no-op.
// Pure struct assembly: no serve.New, no harness, no network.
func TestBuildServeConfigWiresBurst(t *testing.T) {
	argv := []string{"burst", "report"}
	key := []byte("pem-encoded-client-key")

	burstBlock := &policy.BurstPolicy{Command: argv}
	cfg := buildServeConfig(serveRunWiring{
		sf: &serveFlags{},
		loaded: config.Loaded{
			ClientKey: key,
			Policy:    policy.Policy{Serve: &policy.ServePolicy{Burst: burstBlock}},
		},
	})

	if !reflect.DeepEqual(cfg.Burst, burstBlock) {
		t.Errorf("cfg.Burst = %+v, want the serve.burst block", cfg.Burst)
	}
	if !bytes.Equal(cfg.ClientKey, key) {
		t.Errorf("cfg.ClientKey = %q, want the loaded client key", cfg.ClientKey)
	}
	cp, ok := cfg.Provider.(*burst.CommandProvider)
	if !ok {
		t.Fatalf("cfg.Provider = %T, want *burst.CommandProvider with a command set", cfg.Provider)
	}
	if !slices.Equal(cp.Argv, argv) {
		t.Errorf("provider Argv = %v, want the configured command %v", cp.Argv, argv)
	}

	// A burst block with no command carries through, but no provider is
	// built: the gate and the loop both key on the command.
	cfgBurst := buildServeConfig(serveRunWiring{
		sf:     &serveFlags{},
		loaded: config.Loaded{ClientKey: key, Policy: policy.Policy{Serve: &policy.ServePolicy{Burst: &policy.BurstPolicy{}}}},
	})
	if cfgBurst.Burst == nil {
		t.Error("cfg.Burst = nil with an empty burst block, want the block")
	}
	if cfgBurst.Provider != nil {
		t.Errorf("cfg.Provider = %T with no command, want nil", cfgBurst.Provider)
	}

	// No serve block at all: nil-safe, nothing burst is wired, and the
	// absent client key reads as empty.
	cfgNone := buildServeConfig(serveRunWiring{sf: &serveFlags{}, loaded: config.Loaded{}})
	if cfgNone.Burst != nil {
		t.Errorf("cfg.Burst = %+v with no serve block, want nil", cfgNone.Burst)
	}
	if cfgNone.Provider != nil {
		t.Errorf("cfg.Provider = %T with no serve block, want nil", cfgNone.Provider)
	}
	if len(cfgNone.ClientKey) != 0 {
		t.Errorf("cfg.ClientKey = %q with no loaded key, want empty", cfgNone.ClientKey)
	}
}
