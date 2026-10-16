package synclog

import (
	"context"
	"testing"
)

// The cut extension's own rules on the fake: forward only, within the log,
// and answered so the caller learns whether its vouch landed.

// TestCutExtensionWritesWithinTheHead pins the bound: a vouch for its own
// confirmed head stands; one naming a sequence the log does not hold vouches
// for history nobody appended, and is refused.
func TestCutExtensionWritesWithinTheHead(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("a")
	written := memAppend(t, log, `["u"]`)

	if _, ok := log.log.epochCut["a"]; ok {
		t.Fatal("an extension appeared before one was published")
	}
	cut := EpochCut{Epoch: 2, Seq: written.Seq}
	if err := log.PublishEpochCut(context.Background(), cut); err != nil {
		t.Fatalf("PublishEpochCut at the head: %v", err)
	}
	if held := log.log.epochCut["a"]; held.Seq != written.Seq || held.Epoch != 2 {
		t.Errorf("stored cut = %+v, want epoch 2 at %d", held, written.Seq)
	}

	if err := log.PublishEpochCut(context.Background(), EpochCut{Epoch: 2, Seq: written.Seq + 1}); err == nil {
		t.Error("a vouch past the log's head came back as a success")
	}
	if held := log.log.epochCut["a"]; held.Seq != written.Seq {
		t.Errorf("stored cut = %+v, want it untouched by the refused vouch", held)
	}
}

// TestCutExtensionOnlyRaises pins forward-only: a stored cut stands until the
// named one raises it, and a backward or inside-the-fence vouch is refused
// rather than silently ignored.
func TestCutExtensionOnlyRaises(t *testing.T) {
	t.Parallel()
	log := NewMemTransport("a")
	written := memAppend(t, log, `["u"]`)
	if err := log.PublishEpochCut(context.Background(), EpochCut{Epoch: 2, Seq: written.Seq}); err != nil {
		t.Fatalf("first extension: %v", err)
	}
	for name, cut := range map[string]EpochCut{
		"backward":  {Epoch: 2, Seq: written.Seq - 1},
		"old epoch": {Epoch: 1, Seq: written.Seq},
	} {
		if err := log.PublishEpochCut(context.Background(), cut); err == nil {
			t.Errorf("a %s extension named against the stored one came back as a success", name)
		}
	}
	if err := log.PublishEpochCut(context.Background(), EpochCut{Epoch: 2, Seq: written.Seq}); err != nil {
		t.Fatalf("the same extension repeated: %v", err)
	}
	if err := log.PublishEpochCut(context.Background(), EpochCut{Epoch: 3, Seq: written.Seq}); err != nil {
		t.Fatalf("a newer epoch's extension at the same head: %v", err)
	}
	if held := log.log.epochCut["a"]; held.Epoch != 3 {
		t.Errorf("stored cut = %+v, want it raised to epoch 3", held)
	}
}
