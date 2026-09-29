package relevo

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// probeBase is the instant every probe test starts its fake clock at.
var probeBase = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// fakeLine is one scripted stdout line at a fixed offset from the probe start.
// The fake moves the clock to that offset before delivering it.
type fakeLine struct {
	at   time.Duration
	line string
}

// fakeExec is the test LineExec: it records argv and dir, feeds one script per
// call, and drives a clock the test installs as rt.Now.
type fakeExec struct {
	scripts [][]fakeLine
	total   time.Duration
	runErr  error
	gitErr  error

	calls    int
	dirs     []string
	argvs    [][]string
	gitCalls [][]string
	now      *time.Time
}

func (f *fakeExec) Run(_ context.Context, dir string, argv []string, onLine func(line []byte)) error {
	if len(argv) > 0 && argv[0] == "git" {
		f.dirs = append(f.dirs, dir)
		f.gitCalls = append(f.gitCalls, argv)
		return f.gitErr
	}

	f.dirs = append(f.dirs, dir)
	f.argvs = append(f.argvs, argv)

	var script []fakeLine
	if f.calls < len(f.scripts) {
		script = f.scripts[f.calls]
	}
	f.calls++

	for _, l := range script {
		*f.now = probeBase.Add(l.at)
		if onLine != nil {
			onLine([]byte(l.line))
		}
	}
	*f.now = probeBase.Add(f.total)

	return f.runErr
}

// probeRuntime builds a runtime with a real store and a fake clock the caller
// advances by writing *now.
func probeRuntime(t *testing.T, setBody string) (Runtime, *time.Time) {
	t.Helper()
	now := probeBase
	rt := Runtime{
		Store:      store.New(t.TempDir()),
		Candidates: candidateSet(t, setBody),
		Latency:    testGateKV(t),
		Now:        func() time.Time { return now },
	}
	return rt, &now
}
