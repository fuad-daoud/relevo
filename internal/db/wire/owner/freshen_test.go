//go:build unix

package owner_test

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

func sendFreshen(t *testing.T, o *servedVerbOwner) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return client.SyncFreshen(ctx, o.sock)
}

func TestOwnerPassesAFreshenHintToTheHook(t *testing.T) {
	got := make(chan struct{}, 1)
	o := newServedOwner(t, nil, func() { got <- struct{}{} })

	if err := sendFreshen(t, o); err != nil {
		t.Fatalf("SyncFreshen: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the hint never reached the hook")
	}
}

func TestOwnerDropsAFreshenHintWithNoHookAndKeepsServing(t *testing.T) {
	o := newServedOwner(t, func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult {
		return &wire.SyncResult{OK: true}
	}, nil)

	if err := sendFreshen(t, o); err != nil {
		t.Fatalf("SyncFreshen with no hook: %v", err)
	}
	res, err := o.send(t, wire.SyncVerbPush, nil)
	if err != nil || !res.OK {
		t.Fatalf("the owner stopped serving after a dropped hint: %+v, %v", res, err)
	}
}

func TestOwnerFreshenHintIsNotBlockedByAVerbInFlight(t *testing.T) {
	inVerb, endVerb := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(endVerb) })
	got := make(chan struct{}, 1)
	o := newServedOwner(t, func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult {
		close(inVerb)
		<-endVerb
		return &wire.SyncResult{OK: true}
	}, func() { got <- struct{}{} })

	go func() { _, _ = o.send(t, wire.SyncVerbPull, nil) }()
	<-inVerb

	if err := sendFreshen(t, o); err != nil {
		t.Fatalf("SyncFreshen: %v", err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("a verb in flight held the hint back")
	}
}

func TestOwnerSurvivesAPanickingFreshenHook(t *testing.T) {
	o := newServedOwner(t, func(context.Context, *wire.SyncVerb, []byte) *wire.SyncResult {
		return &wire.SyncResult{OK: true}
	}, func() { panic("boom") })

	if err := sendFreshen(t, o); err != nil {
		t.Fatalf("SyncFreshen: %v", err)
	}
	if res, err := o.send(t, wire.SyncVerbPush, nil); err != nil || !res.OK {
		t.Fatalf("the owner died with the hook: %+v, %v", res, err)
	}
}
