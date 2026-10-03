package relevo

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestShowTranscriptDefaultsToTheOpenMirroredRound pins the transcript
// default: a server-chain member installs its rounds only at close, so with no
// --round a transcript read must prefer the open round's mirrored log over the
// newest closed round.
func TestShowTranscriptDefaultsToTheOpenMirroredRound(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 1, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	builder := chainBinding(t, rt, "shop")
	if builder.Round != 2 {
		t.Fatalf("builder round = %d, want 2 after round 1 closed", builder.Round)
	}

	const openLog = "the open round's mirrored log\n"
	fr.getBindingResp = remote.BindingView{Name: "shop", Round: 2, RoundState: remote.RoundRunning}
	fr.roundFileFromFunc = func(context.Context, string, string, int, string, int64) (io.ReadCloser, remote.FileRange, error) {
		return io.NopCloser(strings.NewReader(openLog)), remote.FileRange{}, nil
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		f := fetchRemote(context.Background(), rt, builder)
		next, _, err := applyRemote(context.Background(), rt, tx, builder, &f)
		if err != nil {
			return err
		}
		return tx.Save(next)
	}); err != nil {
		t.Fatalf("observe the open round: %v", err)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "shop", Section: ShowTranscript})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2 (the open, mirrored round)", res.Round)
	}
	if res.Text != openLog {
		t.Errorf("Text = %q, want the open round's mirrored log %q", res.Text, openLog)
	}
}

// TestShowTranscriptOnRunningServerChainReaderWithNoMirroredRounds pins the
// explicit-round half: a running server-chain reader member installs its rounds
// only at close, so with zero mirrored closed rounds an explicit
// --round 1 --transcript must answer from the open round's mirrored log rather
// than refuse with round_not_found on a prompt-entry count of zero.
func TestShowTranscriptOnRunningServerChainReaderWithNoMirroredRounds(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusRunning), 0, 0, 0))
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	reader := chainBinding(t, rt, "shop-rev")
	if reader.Shape != store.ShapeReader {
		t.Fatalf("reader shape = %q, want %q", reader.Shape, store.ShapeReader)
	}
	if reader.Round != 1 {
		t.Fatalf("reader round = %d, want 1 with no closed round", reader.Round)
	}

	const openLog = "the reader's open round log\n"
	fr.getBindingResp = remote.BindingView{Name: "shop-rev", Round: 1, RoundState: remote.RoundRunning}
	fr.roundFileFromFunc = func(context.Context, string, string, int, string, int64) (io.ReadCloser, remote.FileRange, error) {
		return io.NopCloser(strings.NewReader(openLog)), remote.FileRange{}, nil
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		f := fetchRemote(context.Background(), rt, reader)
		next, _, err := applyRemote(context.Background(), rt, tx, reader, &f)
		if err != nil {
			return err
		}
		return tx.Save(next)
	}); err != nil {
		t.Fatalf("observe the reader's open round: %v", err)
	}

	// The read-back is the mirror proof, in the shape of
	// assertServerChainMemberLive: the chain pull installed nothing here, so
	// these bytes came from the observe path alone and are what Show will find.
	body, err := rt.Store.ReadFile(rt.Store.BuilderLogPath("shop-rev", 1))
	if err != nil {
		t.Fatalf("read mirrored reader log: %v", err)
	}
	if string(body) != openLog {
		t.Fatalf("mirrored reader log = %q, want %q", body, openLog)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "shop-rev", Section: ShowTranscript, Round: 1})
	if err != nil {
		t.Fatalf("Show --round 1 --transcript: %v", err)
	}
	if res.Round != 1 {
		t.Errorf("Round = %d, want 1 (the open round)", res.Round)
	}
	if res.Text != openLog {
		t.Errorf("Text = %q, want the reader's open round log %q", res.Text, openLog)
	}
}
