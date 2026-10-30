package verb

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

func joinVerbWithR2(t *testing.T, endpoint string) (*wire.SyncVerb, []byte) {
	t.Helper()
	verb := joinVerb(t, joinPassphrase)
	verb.R2Endpoint, verb.R2Bucket, verb.R2KeyID = endpoint, verbFixtureR2.Bucket, verbFixtureR2.KeyID
	verb.R2SecretLen = len(verbFixtureR2.Secret)
	return verb, []byte(joinToken + joinPassphrase + verbFixtureR2.Secret)
}

// keylessFleet is a remote whose fleet never shared bucket credentials.
func keylessFleet(t *testing.T) (*Runner, relevosync.Local) {
	t.Helper()
	runner, local := joinRunner(t)
	log := synclog.NewMemTransport("m1")
	if _, err := log.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	runner.Open = func(context.Context) (synclog.LogTransport, error) { return log, nil }
	return runner, local
}

func TestJoinWithBucketFlagsAgainstACredentialLessFleetHoldsTheFlagCredentials(t *testing.T) {
	t.Parallel()
	runner, local := keylessFleet(t)
	verb, tail := joinVerbWithR2(t, verbFixtureR2.Endpoint)
	res := runner.Run(context.Background(), verb, tail)
	if !res.OK {
		t.Fatalf("join with bucket flags refused: (%s) %s", res.Code, res.Message)
	}
	stored, err := relevosync.ReadR2(local)
	if err != nil || stored != verbFixtureR2 {
		t.Errorf("stored R2 = %+v (%v), want the flags' credentials", stored, err)
	}
}

func TestJoinWithPartialBucketFlagsAndNoDocumentEndsInNoR2(t *testing.T) {
	t.Parallel()
	runner, _ := keylessFleet(t)
	verb := joinVerb(t, joinPassphrase)
	verb.R2Bucket = verbFixtureR2.Bucket
	res := runner.Run(context.Background(), verb, []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("partial flags = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
}

func TestJoinWithABadBucketEndpointIsRefused(t *testing.T) {
	t.Parallel()
	runner, _ := keylessFleet(t)
	verb, tail := joinVerbWithR2(t, "http://not-allowed.invalid")
	res := runner.Run(context.Background(), verb, tail)
	if res.OK {
		t.Fatal("a join with an invalid bucket endpoint was accepted")
	}
}
