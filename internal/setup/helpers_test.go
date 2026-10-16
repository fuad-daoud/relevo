package setup

import (
	"fmt"
	"io/fs"
	"strings"
	"time"
)

// pathEnv is the InstallEnv seam the fakes embed; only LookPath answers.
type pathEnv struct{ onPath map[string]bool }

func (e pathEnv) LookPath(binary string) (string, error) {
	if e.onPath[binary] {
		return "/bin/" + binary, nil
	}
	return "", fmt.Errorf("binary not found: %s", binary)
}

func (e pathEnv) HomePath(rel string) (string, error)      { return rel, nil }
func (e pathEnv) ReadFile(string) ([]byte, error)          { return nil, fs.ErrNotExist }
func (e pathEnv) MkdirAll(string) error                    { return nil }
func (e pathEnv) WriteFile(string, []byte) error           { return nil }
func (e pathEnv) LoadManifest() (map[string]string, error) { return map[string]string{}, nil }
func (e pathEnv) SaveManifest(map[string]string) error     { return nil }

// probeEnv is the SetupEnv the tests build: which binaries are on PATH and
// what the runner answers per call. probeT bounds the probe inside the test,
// so a plan that blocks exercises the timeout path on the test's own clock;
// zero keeps the real deadline.
type probeEnv struct {
	pathEnv
	probeT time.Duration
	run    func(name string, args ...string) ([]byte, int, error)
}

func (e probeEnv) Run(name string, args ...string) ([]byte, int, error) {
	if e.run == nil {
		return nil, -1, fmt.Errorf("unexpected probe command %s", callKey(name, args...))
	}
	return e.run(name, args...)
}

func (e probeEnv) ProbeTimeout() time.Duration {
	if e.probeT == 0 {
		return probeDeadline
	}
	return e.probeT
}

// callKey joins the tokens one probe call carries, so a fake switches on the
// full command instead of only the harness name.
func callKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// status is one inventory row a builder of inventories uses, with the models
// the kind offers when logged in.
func status(kind string, loggedIn bool, models ...string) HarnessStatus {
	s := HarnessStatus{Harness: kind, OnPath: true, Login: loginCommands[kind], Models: models}
	if loggedIn {
		s.LoggedIn = true
	}
	return s
}
