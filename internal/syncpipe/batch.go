package syncpipe

// The batch verdicts a blob call answers with: one result per key, each mapped
// like a whole-call refusal, and the batch's own error for the breaker.

import (
	"fmt"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// batchRequest names every item's key, staging file and size limit beside one
// verb, where a single call names them on the request itself.
func batchRequest(verb syncworker.Verb, items []synclog.BlobBatchItem) syncworker.Request {
	blobs := make([]syncworker.BlobItem, len(items))
	for n, item := range items {
		blobs[n] = syncworker.BlobItem{Key: item.Key, Staging: item.Staging, Max: item.Max}
	}
	return syncworker.Request{Verb: verb, Blobs: blobs}
}

// batchVerdicts reads one batch reply into a verdict per key, mapping each
// refusal code to its sentinel exactly as a whole-call refusal is mapped, and
// returns the batch's refusal for the breaker: the first permanent failure in
// key order, otherwise the first failed key. A reply that answers some other
// number of keys than were asked is a worker out of step, so it ends the pipe
// the way a reply with the wrong id does rather than leaving a verdict a
// caller would have to guess at.
func (c *Client) batchVerdicts(verb syncworker.Verb, resp syncworker.Response,
	items []synclog.BlobBatchItem) ([]synclog.BlobBatchResult, error) {
	if len(resp.Results) != len(items) {
		return nil, c.miss(fmt.Errorf("%s answered %d results for %d keys",
			verb, len(resp.Results), len(items)))
	}
	out := make([]synclog.BlobBatchResult, len(items))
	var firstFailed, firstPermanent error
	for n, r := range resp.Results {
		verdict := synclog.BlobBatchResult{Key: items[n].Key, Bytes: r.Bytes, Skipped: r.Skipped}
		if r.Error != "" {
			verdict.Err = refusalError(syncworker.Response{Error: r.Error, Code: r.Code})
			if firstFailed == nil {
				firstFailed = verdict.Err
			}
			if firstPermanent == nil && relevosync.IsPermanentRefusal(verdict.Err) {
				firstPermanent = verdict.Err
			}
		}
		out[n] = verdict
	}
	if firstPermanent != nil {
		return out, firstPermanent
	}
	return out, firstFailed
}
