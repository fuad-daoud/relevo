package owner

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire/client"
)

// TestConnCountExcludesAClientThatSawItsClose pins the order of a connection's
// teardown: client.Info returns once the owner closes the socket, and a dial
// reads the count straight after through the next handshake, so the owner
// must have dropped the connection from its live count before that close.
func TestConnCountExcludesAClientThatSawItsClose(t *testing.T) {
	srv, sock := startServer(t)
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.Info(ctx, sock)
		cancel()
		if err != nil {
			t.Fatalf("Info %d: %v", i, err)
		}
		if got := srv.ConnCount(); got != 0 {
			t.Fatalf("after handshake %d returned, ConnCount = %d, want 0", i, got)
		}
	}
}
