package mastermind

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// initAt builds a hook-shaped InitInput for one host and session.
func initAt(host int, session string, now time.Time) InitInput {
	return InitInput{
		Kind:          "claude",
		SessionID:     session,
		CWD:           "/tmp/relevo-mastermind-test",
		Agent:         "architect",
		HostPID:       host,
		HostStartedAt: int64(host) * 10,
		Now:           now,
	}
}

// TestInitCreatesThenReattachesSameHost is the /clear case: the same host
// process keeps its mastermind id across a new session id.
func TestInitCreatesThenReattachesSameHost(t *testing.T) {
	reg := testRegistry(t)

	first, res, err := Init(reg, initAt(100, "sess-1", testNow))
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if res != InitCreated {
		t.Fatalf("first Init result = %q, want %q", res, InitCreated)
	}

	second, res, err := Init(reg, initAt(100, "sess-2", testNow.Add(time.Minute)))
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if res != InitMoved {
		t.Errorf("second Init result = %q, want %q", res, InitMoved)
	}
	if second.ID != first.ID {
		t.Errorf("second Init minted %s, want the same id %s", second.ID, first.ID)
	}
	if second.SessionID != "sess-2" {
		t.Errorf("SessionID = %q, want sess-2", second.SessionID)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].SessionID != "sess-1" {
		t.Errorf("sessions = %+v, want sess-1 once", second.Sessions)
	}
	if got, want := second.Sessions[0].To, testNow.Add(time.Minute); !got.Equal(want) {
		t.Errorf("history To = %v, want %v", got, want)
	}
	if !second.SeenAt.Equal(testNow.Add(time.Minute)) {
		t.Errorf("SeenAt = %v, want the init time", second.SeenAt)
	}

	records, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("registry holds %d records, want 1", len(records))
	}
}

// TestInitReattachesBySessionInNewProcess is --resume in a fresh process.
func TestInitReattachesBySessionInNewProcess(t *testing.T) {
	reg := testRegistry(t)

	first, _, err := Init(reg, initAt(100, "sess-1", testNow))
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}

	second, res, err := Init(reg, initAt(200, "sess-1", testNow.Add(time.Minute)))
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if res != InitReattached {
		t.Errorf("second Init result = %q, want %q", res, InitReattached)
	}
	if second.ID != first.ID {
		t.Errorf("second Init minted %s, want the same id %s", second.ID, first.ID)
	}
	if second.HostPID != 200 || second.HostStartedAt != 2000 {
		t.Errorf("host = %d@%d, want 200@2000", second.HostPID, second.HostStartedAt)
	}
	if len(second.Sessions) != 0 {
		t.Errorf("sessions = %+v, want none: the session did not change", second.Sessions)
	}
}

// TestInitReusesPriorDBID pins the upgrade path: a database row already
// present for (kind, session) donates its id rather than a fresh one.
func TestInitReusesPriorDBID(t *testing.T) {
	cases := []struct {
		name    string
		priorID string
	}{
		{name: "legacy ULID id", priorID: "01M3252956S27X5G5MPVM77PJ7"},
		{name: "minted pl_ id", priorID: "pl_zzzzzzzzzzzz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := testRegistry(t)

			in := initAt(100, "sess-1", testNow)
			in.PriorID = func(kind, session string) (string, bool) {
				if kind == "claude" && session == "sess-1" {
					return tc.priorID, true
				}
				return "", false
			}

			rec, res, err := Init(reg, in)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if res != InitCreated {
				t.Errorf("result = %q, want %q", res, InitCreated)
			}
			if rec.ID != tc.priorID {
				t.Errorf("id = %q, want the prior db id %q", rec.ID, tc.priorID)
			}

			if stored, err := reg.Get(tc.priorID); err != nil || stored.ID != tc.priorID {
				t.Errorf("Get(%q) = %+v, %v", tc.priorID, stored, err)
			}

			// A prior id for another session is not consulted.
			in.SessionID = "sess-2"
			if _, ok := in.PriorID("claude", "sess-2"); ok {
				t.Fatal("test fixtures disagree")
			}
			second, res, err := Init(reg, in)
			if err != nil {
				t.Fatalf("second Init: %v", err)
			}
			if res != InitMoved || second.ID != tc.priorID {
				t.Errorf("second Init = %q/%s, want moved/%s", res, second.ID, tc.priorID)
			}
		})
	}
}

// TestInitExplicitRegistrationHasNoHost pins `--kind/--session`: a record
// registered by hand has host_pid 0 and re-attaches by session.
func TestInitExplicitRegistrationHasNoHost(t *testing.T) {
	reg := testRegistry(t)

	in := InitInput{
		Kind:      "opencode",
		SessionID: "ses_abc123",
		CWD:       "/tmp/relevo-mastermind-test",
		Now:       testNow,
	}
	first, res, err := Init(reg, in)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if res != InitCreated {
		t.Fatalf("result = %q, want %q", res, InitCreated)
	}
	if first.HostPID != 0 || first.HostStartedAt != 0 {
		t.Errorf("host = %d@%d, want 0@0", first.HostPID, first.HostStartedAt)
	}
	if first.Name != "opencode-1" {
		t.Errorf("Name = %q, want opencode-1", first.Name)
	}

	// Re-registering by hand re-attaches without clearing the host a hook gave.
	if _, err := reg.SetHost(first.ID, 777, 7770); err != nil {
		t.Fatalf("SetHost: %v", err)
	}
	in.Now = testNow.Add(time.Minute)
	second, res, err := Init(reg, in)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if res != InitReattached || second.ID != first.ID {
		t.Errorf("second Init = %q/%s, want reattached/%s", res, second.ID, first.ID)
	}
	if second.HostPID != 777 {
		t.Errorf("HostPID = %d, want the existing 777", second.HostPID)
	}
}

// TestInitRenamesWhenNameGiven pins that a non-empty Name renames an
// existing record.
func TestInitRenamesWhenNameGiven(t *testing.T) {
	reg := testRegistry(t)

	first, _, err := Init(reg, initAt(100, "sess-1", testNow))
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}

	in := initAt(100, "sess-1", testNow.Add(time.Minute))
	in.Name = "reviewer-2"
	second, res, err := Init(reg, in)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if res != InitReattached || second.ID != first.ID || second.Name != "reviewer-2" {
		t.Errorf("second Init = %q/%s/%q, want reattached/%s/reviewer-2", res, second.ID, second.Name, first.ID)
	}

	other, _, err := Init(reg, initAt(200, "sess-2", testNow))
	if err != nil {
		t.Fatalf("third Init: %v", err)
	}
	in = initAt(100, "sess-1", testNow.Add(2*time.Minute))
	in.Name = other.Name
	if _, _, err := Init(reg, in); !errors.Is(err, ErrNameTaken) {
		t.Errorf("Init renaming onto a taken name = %v, want ErrNameTaken", err)
	}
}

// TestConcurrentInitSerialises pins the transaction rule: two registries
// racing on one database must end with exactly one record.
func TestConcurrentInitSerialises(t *testing.T) {
	d := testDB(t)

	const racers = 2
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []string
		errs []error
	)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		reg := testRegistryOn(t, d)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec, _, err := Init(reg, initAt(100, "sess-1", testNow))

			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, rec.ID)
			errs = append(errs, err)
		}()
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
		}
	}

	records, err := testRegistryOn(t, d).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("registry holds %d records, want exactly 1", len(records))
	}
	for i, id := range ids {
		if id != records[0].ID {
			t.Errorf("racer %d resolved %s, want the one record %s", i, id, records[0].ID)
		}
	}
}
