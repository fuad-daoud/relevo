//go:build !unix

package client

import (
	"context"
	"testing"
)

func TestUnsupportedRefusesToDial(t *testing.T) {
	if _, err := Info(context.Background(), "/tmp/nowhere.sock"); err == nil {
		t.Fatal("Info succeeded on a platform without unix sockets")
	}
}
