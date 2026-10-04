package sync

import (
	"context"
)

// Stats is what the remote reports about the local change set: the operations a
// push has not sent yet, the last successful push and pull, the bytes each way,
// and the server revision. The revision is opaque and must never be parsed.
type Stats struct {
	CdcOperations        int64  `json:"cdc_operations"`
	LastPullUnixTime     int64  `json:"last_pull_unix_time"`
	LastPushUnixTime     int64  `json:"last_push_unix_time"`
	NetworkSentBytes     int64  `json:"network_sent_bytes"`
	NetworkReceivedBytes int64  `json:"network_received_bytes"`
	Revision             string `json:"revision"`
}

// SyncClient is the one surface sync drives, so a test can hand a fake in place
// of a remote and no test needs a network. Push sends the local change set and
// fetches nothing; Pull fetches remote changes, rebases the local ones on top
// and reports whether anything was applied; Stats reports the change set;
// Checkpoint truncates the local write-ahead log.
type SyncClient interface {
	Push(ctx context.Context) error
	Pull(ctx context.Context) (bool, error)
	Stats(ctx context.Context) (Stats, error)
	Checkpoint(ctx context.Context) error
}
