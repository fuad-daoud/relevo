//go:build unix

package client_test

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

func TestClientSyncFreshenSendsTheHintFrame(t *testing.T) {
	l, sock := shortListener(t)
	kinds := make(chan byte, 4)
	go func() {
		nc, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = nc.Close() }()
		w := wire.NewConn(nc)
		if _, err := w.Read(); err != nil {
			return
		}
		welcome, _ := wire.Encode(wire.KindWelcome, &wire.Welcome{Header: wire.Header{Type: wire.TypeWelcome}, Proto: wire.Proto}, nil)
		if w.Write(welcome) != nil {
			return
		}
		for {
			frame, err := w.Read()
			if err != nil {
				return
			}
			kind, _ := wire.Kind(frame)
			kinds <- kind
			if kind == wire.KindClose {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.SyncFreshen(ctx, sock); err != nil {
		t.Fatalf("SyncFreshen: %v", err)
	}
	select {
	case got := <-kinds:
		if got != wire.KindSyncFreshen {
			t.Errorf("first frame kind = %d, want %d", got, wire.KindSyncFreshen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame reached the owner")
	}
}

func TestClientSyncFreshenFailsQuietlyWithNoOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.SyncFreshen(ctx, t.TempDir()+"/nothing.sock"); err == nil {
		t.Fatal("an unserved socket reported success")
	}
}
