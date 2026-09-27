package relevo

import (
	"errors"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// ErrNoClientKey reports a servers section with no stored client key. The key
// is the only thing that makes a configured server usable, so the text names
// the fix and is printed verbatim by both callers.
var ErrNoClientKey = errors.New("servers configured but no client key; run relevo config server key")

// NewRemoteClient turns a servers section and the stored client key into the
// client every remote path uses. Construction touches no network, so a running
// daemon may call it again whenever the section or the key changes.
func NewRemoteClient(servers remote.Servers, key []byte) (RemoteClient, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	if len(key) == 0 {
		return nil, ErrNoClientKey
	}
	kp, err := remote.ParsePrivate(key)
	if err != nil {
		return nil, err
	}
	return client.New(servers, kp, time.Now), nil
}
