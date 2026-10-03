package remote

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

var update = flag.Bool("update", false, "update golden files")

var (
	fixedTime  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fixedNonce = "nonce-fixed-0001"
)

func fixedKeypair() Keypair {
	var seed [32]byte
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	return Keypair{Private: priv, Public: pub}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./internal/remote -run Contract -update' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./internal/remote -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s", path, string(got), string(want))
	}
}

func filledFileRange() FileRange {
	return FileRange{
		Honored: true,
		From:    1024,
		Size:    2048,
	}
}

func filledWhoAmI() WhoAmI {
	builders := filledBuildersView()
	return WhoAmI{
		ID:                ClientID("SHA256:fixed-test-client-id-00000000000000000000000"),
		Label:             "test-client",
		ServerVersion:     1,
		Transports:        []string{"git-bundle"},
		Features:          []string{"tier", "queue", "stop", "readers"},
		BuilderTier:       "edit",
		MaxTier:           "yolo",
		Builders:          &builders,
		Installation:      "01SERVERINSTALLATION0000000",
		InstallationLabel: "zen",
	}
}

func filledGitIdentity() GitIdentity {
	return GitIdentity{
		Name:  "Test Builder",
		Email: "builder@example.com",
	}
}

func filledCreateBindingRequest() CreateBindingRequest {
	author := filledGitIdentity()
	return CreateBindingRequest{
		Name:               "test-binding",
		RepoID:             "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BaseCommit:         "1111222233334444555566667777888899990000",
		Candidate:          "agy/openai/gpt-4",
		RoundCap:           10,
		RoundTimeoutMS:     60000,
		Tier:               "edit",
		Role:               "builder",
		Feature:            "auth",
		Ticket:             "o/r#607",
		Gate:               "make check",
		Author:             &author,
		ClientInstallation: "01CLIENTINSTALLATION0000000",
		ClientBindingID:    "01CLIENTBINDINGRECORD000000",
	}
}

func filledArtifactFile() ArtifactFile {
	return ArtifactFile{
		Rel:   "site/index.html",
		Size:  1024,
		MTime: fixedTime,
	}
}

func filledArtifactList() ArtifactList {
	return ArtifactList{
		Actor:  "reviewer",
		Output: "findings.md",
		Files:  []ArtifactFile{filledArtifactFile()},
	}
}

func filledTagRef() TagRef {
	return TagRef{
		Name: "v1.2.3",
		SHA:  "2222333344445555666677778888999900001111",
	}
}

func filledQueueView() QueueView {
	return QueueView{
		Position: 2,
		Ahead:    1,
		Running:  3,
		Cap:      5,
		Since:    fixedTime,
	}
}

func filledDiffStat() DiffStat {
	return DiffStat{
		Files:   3,
		Added:   42,
		Removed: 7,
	}
}

func filledUsage() usage.Usage {
	return usage.Usage{
		Harness:    "opencode",
		Provider:   "anthropic",
		Model:      "claude-sonnet-5",
		DurationMS: 12500,
		Tokens: usage.Tokens{
			In:         1000,
			CacheRead:  3000,
			CacheWrite: 500,
			Out:        250,
		},
		Cost: usage.Cost{
			USD:   0.05,
			Basis: usage.Measured,
			Plan:  true,
		},
		Samples:          2,
		Steps:            5,
		ToolCalls:        3,
		StepP50MS:        1200,
		FirstOutputP50MS: 400,
		Note:             "sample note",
	}
}

func filledLiveView() LiveView {
	u := filledUsage()
	diff := filledDiffStat()
	return LiveView{
		At:             fixedTime,
		PID:            4242,
		StartedAt:      fixedTime,
		ExitCode:       "0",
		Tail:           []string{"output line 1", "output line 2"},
		Usage:          &u,
		PriorTokens:    usage.Tokens{In: 100, CacheRead: 200, CacheWrite: 50, Out: 25},
		Diff:           &diff,
		LastProgressAt: fixedTime,
		ExploringSince: fixedTime,
		GatingSince:    fixedTime,
	}
}

func filledBuildersView() BuildersView {
	return BuildersView{
		Running:   2,
		Queued:    1,
		Cap:       4,
		Scopes:    true,
		Slice:     "relevo.slice",
		Quota:     "200%",
		Isolation: "container",
		Image:     "registry.example/relevo-builder:latest",
	}
}

func filledBindingView() BindingView {
	u := filledUsage()
	rusage := store.Rusage{CPUMS: 2500, PeakMemBytes: 52428800}
	prior := usage.Tokens{In: 50, CacheRead: 100, CacheWrite: 20, Out: 10}
	q := filledQueueView()
	live := filledLiveView()
	return BindingView{
		Name:          "test-binding",
		State:         "active",
		Round:         2,
		RoundState:    RoundRunning,
		ClosedRound:   1,
		Halt:          "user-requested halt",
		ResultCommit:  "3333444455556666777788889999000011112222",
		DirtyCommit:   "4444555566667777888899990000111122223333",
		ReportOutcome: "completed",
		GateResult:    "pass",
		Stopped:       "killed",
		ReportNote:    "noreport unmarked",
		Switches: []string{
			"switched builder (rate-limited: 429): picked agy/test/m for builder: order #2",
			"switched builder (rate-limited: 429): picked claude/test/m for builder: order #5",
		},
		Shape:          "reader",
		DiffNote:       "refactored remote wire",
		DiffCommits:    2,
		DiffTree:       "5555666677778888999900001111222233334444",
		AckedRound:     1,
		Candidate:      "agy/openai/gpt-4",
		Account:        "work",
		RoundStartedAt: fixedTime,
		RoundCap:       10,
		RoundTimeoutMS: 60000,
		Tier:           "edit",
		Feature:        "auth",
		Ticket:         "o/r#607",
		Usage:          &u,
		Rusage:         &rusage,
		PriorTokens:    &prior,
		StalledSince:   fixedTime,
		Queue:          &q,
		Live:           &live,
		ID:             "01SERVERBINDINGRECORD00000000",
		Installation:   "01SERVERINSTALLATION0000000",
	}
}

func filledUnavailableRequest() UnavailableRequest {
	return UnavailableRequest{
		Token:  "agy/openai/gpt-4",
		Reason: "capacity limit exceeded",
	}
}

func filledAvailableRequest() AvailableRequest {
	return AvailableRequest{
		Subject: "agy/openai/gpt-4",
	}
}

func filledAvailableResponse() AvailableResponse {
	return AvailableResponse{
		Provider: "openai",
		Removed:  3,
	}
}

func filledCandidateView() CandidateView {
	return CandidateView{
		Token: "agy/openai/gpt-4",
		Name:  "gpt-4",
		Kind:  "agy",
		Gated: true,
		Pick:  true,
	}
}

func filledCandidatesResponse() CandidatesResponse {
	return CandidatesResponse{
		Candidates: []CandidateView{filledCandidateView()},
	}
}

func filledActorView() ActorView {
	return ActorView{
		Actor:      "builder",
		Shape:      "writer",
		Accepted:   true,
		Pick:       "agy/openai/gpt-4",
		Reason:     "",
		Candidates: []CandidateView{filledCandidateView()},
	}
}

func filledErrorBody() ErrorBody {
	return ErrorBody{
		Code:    CodeNotEnrolled,
		Message: "unknown client key",
	}
}

func filledChainSettings() ChainSettings {
	return ChainSettings{
		MaxCorrections: 3,
		ReviewerActor:  "reviewer",
		PlannerActor:   "planner",
		SecurityActor:  "security",
		Security:       true,
		Gate:           "make check",
		Regate:         2,
	}
}

func filledCreateChainRequest() CreateChainRequest {
	author := filledGitIdentity()
	return CreateChainRequest{
		Name:               "test-chain",
		RepoID:             "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BaseCommit:         "1111222233334444555566667777888899990000",
		Plans:              []string{"# Plan one\n", "# Plan two\n"},
		Settings:           filledChainSettings(),
		Feature:            "auth",
		Ticket:             "o/r#607",
		Author:             &author,
		ClientInstallation: "01CLIENTINSTALLATION0000000",
		ClientBindingIDs: map[string]string{
			"builder":  "01CLIENTBINDINGBUILDER000000",
			"reviewer": "01CLIENTBINDINGREVIEWER00000",
		},
		Workflow: json.RawMessage(`{"version":1,"steps":[{"name":"build","actor":"builder"}]}`),
		ClientActorIDs: map[string]string{
			"builder":  "01CLIENTACTORBUILDER0000000",
			"reviewer": "01CLIENTACTORREVIEWER000000",
		},
	}
}

func filledClosedRoundView() ClosedRoundView {
	u := filledUsage()
	rusage := store.Rusage{CPUMS: 2500, PeakMemBytes: 52428800}
	return ClosedRoundView{
		Round:         2,
		ReportOutcome: "completed",
		GateResult:    "pass",
		Stopped:       "killed",
		ReportNote:    "noreport unmarked",
		Switches: []string{
			"switched builder (rate-limited: 429): picked agy/test/m for builder: order #2",
			"switched builder (rate-limited: 429): picked claude/test/m for builder: order #5",
		},
		DiffNote:    "refactored remote wire",
		DiffCommits: 2,
		DiffTree:    "5555666677778888999900001111222233334444",
		Usage:       &u,
		Rusage:      &rusage,
	}
}

func filledChainMemberView() ChainMemberView {
	return ChainMemberView{
		Part:   "builder",
		Name:   "test-binding",
		Actor:  "builder",
		View:   filledBindingView(),
		Rounds: []ClosedRoundView{filledClosedRoundView()},
	}
}

func filledChainEventView() ChainEventView {
	return ChainEventView{
		Seq:    3,
		TS:     fixedTime,
		Phase:  "build",
		Step:   "reviewing",
		Member: "builder",
		Round:  2,
		Plan:   1,
		Event:  `{"kind":"builder_closed","gate":"green"}`,
		Action: `{"member":"reviewer","round":2}`,
		Reason: "check red after regate",
	}
}

func filledChainView() ChainView {
	return ChainView{
		Name:            "test-chain",
		Status:          "running",
		Phase:           "build",
		Step:            "reviewing",
		Plan:            2,
		Plans:           4,
		Corrections:     1,
		AwaitingMember:  "reviewer",
		AwaitingRound:   2,
		Base:            "1111222233334444555566667777888899990000",
		Branch:          "relevo/test-chain",
		Feature:         "auth",
		Ticket:          "o/r#607",
		PlanStartCommit: "6666777788889999000011112222333344445555",
		Settings:        filledChainSettings(),
		Findings:        1,
		Members:         []ChainMemberView{filledChainMemberView()},
		Trace:           []ChainEventView{filledChainEventView()},
		Workflow:        json.RawMessage(`{"version":1,"steps":[{"name":"build","actor":"builder"}]}`),
		State:           json.RawMessage(`{"phase":"build","step":"reviewing"}`),
	}
}

func filledChainResumeRequest() ChainResumeRequest {
	maxCorrections := 2
	security := true
	gate := "make check"
	regate := 1
	return ChainResumeRequest{
		MaxCorrections: &maxCorrections,
		ReviewerActor:  "reviewer",
		PlannerActor:   "planner",
		SecurityActor:  "security",
		Security:       &security,
		Gate:           &gate,
		Regate:         &regate,
	}
}

func filledChainStopResponse() ChainStopResponse {
	return ChainStopResponse{
		Round:  2,
		Action: "stopped",
		Chain:  filledChainView(),
	}
}

func filledCreateCheckRequest() CreateCheckRequest {
	return CreateCheckRequest{
		ID:      "01CHECKRUN000000000000000000",
		Command: "make check",
		Step:    "reviewing",
	}
}

func filledCheckView() CheckView {
	return CheckView{
		ID:           "01CHECKRUN000000000000000000",
		Command:      "make check",
		Step:         "reviewing",
		Result:       "pass",
		ExitCode:     1,
		DurationMS:   1250,
		Note:         "acceptance passed",
		LogTail:      "all tests pass",
		LogTruncated: true,
	}
}

func filledSetGateRequest() SetGateRequest {
	gate := "make check"
	regate := 2
	return SetGateRequest{
		Gate:   &gate,
		Regate: &regate,
	}
}

type protoTypeCase struct {
	name string
	val  any
	zero func() any
}

var protoCases = []protoTypeCase{
	{"FileRange", filledFileRange(), func() any { return new(FileRange) }},
	{"WhoAmI", filledWhoAmI(), func() any { return new(WhoAmI) }},
	{"GitIdentity", filledGitIdentity(), func() any { return new(GitIdentity) }},
	{"CreateBindingRequest", filledCreateBindingRequest(), func() any { return new(CreateBindingRequest) }},
	{"TagRef", filledTagRef(), func() any { return new(TagRef) }},
	{"ArtifactFile", filledArtifactFile(), func() any { return new(ArtifactFile) }},
	{"ArtifactList", filledArtifactList(), func() any { return new(ArtifactList) }},
	{"BindingView", filledBindingView(), func() any { return new(BindingView) }},
	{"QueueView", filledQueueView(), func() any { return new(QueueView) }},
	{"DiffStat", filledDiffStat(), func() any { return new(DiffStat) }},
	{"LiveView", filledLiveView(), func() any { return new(LiveView) }},
	{"BuildersView", filledBuildersView(), func() any { return new(BuildersView) }},
	{"UnavailableRequest", filledUnavailableRequest(), func() any { return new(UnavailableRequest) }},
	{"AvailableRequest", filledAvailableRequest(), func() any { return new(AvailableRequest) }},
	{"AvailableResponse", filledAvailableResponse(), func() any { return new(AvailableResponse) }},
	{"CandidateView", filledCandidateView(), func() any { return new(CandidateView) }},
	{"CandidatesResponse", filledCandidatesResponse(), func() any { return new(CandidatesResponse) }},
	{"ActorView", filledActorView(), func() any { return new(ActorView) }},
	{"ChainSettings", filledChainSettings(), func() any { return new(ChainSettings) }},
	{"CreateChainRequest", filledCreateChainRequest(), func() any { return new(CreateChainRequest) }},
	{"ClosedRoundView", filledClosedRoundView(), func() any { return new(ClosedRoundView) }},
	{"ChainMemberView", filledChainMemberView(), func() any { return new(ChainMemberView) }},
	{"ChainEventView", filledChainEventView(), func() any { return new(ChainEventView) }},
	{"ChainView", filledChainView(), func() any { return new(ChainView) }},
	{"ChainResumeRequest", filledChainResumeRequest(), func() any { return new(ChainResumeRequest) }},
	{"ChainStopResponse", filledChainStopResponse(), func() any { return new(ChainStopResponse) }},
	{"CreateCheckRequest", filledCreateCheckRequest(), func() any { return new(CreateCheckRequest) }},
	{"CheckView", filledCheckView(), func() any { return new(CheckView) }},
	{"SetGateRequest", filledSetGateRequest(), func() any { return new(SetGateRequest) }},
	{"ErrorBody", filledErrorBody(), func() any { return new(ErrorBody) }},
}

func TestContractProtoShapes(t *testing.T) {
	for _, tc := range protoCases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.MarshalIndent(tc.val, "", "  ")
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			b = append(b, '\n')
			assertGolden(t, "proto-"+tc.name, b)

			back := tc.zero()
			if err := json.Unmarshal(b, back); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.name, err)
			}
			b2, err := json.MarshalIndent(back, "", "  ")
			if err != nil {
				t.Fatalf("re-marshal %s: %v", tc.name, err)
			}
			b2 = append(b2, '\n')
			if !bytes.Equal(b, b2) {
				t.Fatalf("%s round-trip mismatch:\n--- orig ---\n%s\n--- back ---\n%s", tc.name, string(b), string(b2))
			}
		})
	}
}

// TestGateFieldsAreAdditive pins the omitempty halves the goldens cannot: a
// zero CreateBindingRequest and BindingView marshal without a gate/gate_result
// key, so an old server parses an old client's body unchanged and an old client
// sees no new key on an old server's view.
func TestGateFieldsAreAdditive(t *testing.T) {
	create, err := json.Marshal(CreateBindingRequest{})
	if err != nil {
		t.Fatalf("marshal zero CreateBindingRequest: %v", err)
	}
	if bytes.Contains(create, []byte(`"gate"`)) {
		t.Fatalf("zero CreateBindingRequest carries a gate key: %s", create)
	}
	view, err := json.Marshal(BindingView{})
	if err != nil {
		t.Fatalf("marshal zero BindingView: %v", err)
	}
	if bytes.Contains(view, []byte(`"gate_result"`)) {
		t.Fatalf("zero BindingView carries a gate_result key: %s", view)
	}
}

// TestChainResumeRequestKeepsAbsentDistinctFromEmpty pins the pointer that
// separates an absent gate from one cleared: an empty request marshals with no
// gate key, while a gate pointing at "" keeps the key.
//
// Mutation: make Gate a plain string with omitempty and the cleared gate is
// dropped.
func TestChainResumeRequestKeepsAbsentDistinctFromEmpty(t *testing.T) {
	absent, err := json.Marshal(ChainResumeRequest{})
	if err != nil {
		t.Fatalf("marshal empty resume request: %v", err)
	}
	if bytes.Contains(absent, []byte(`"gate"`)) {
		t.Fatalf("absent gate marshaled a key: %s", absent)
	}

	var empty ChainResumeRequest
	if err := json.Unmarshal([]byte(`{"gate":""}`), &empty); err != nil {
		t.Fatalf("unmarshal cleared gate: %v", err)
	}
	out, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal cleared gate: %v", err)
	}
	if !bytes.Contains(out, []byte(`"gate":""`)) {
		t.Fatalf("gate pointing at \"\" marshaled %s, want a gate key", out)
	}
}

// TestCreateChainRequestWorkflowOmittedWhenEmpty pins that a create request
// without workflow fields matches the pre-workflow JSON shape byte-for-byte.
//
// Mutation: drop omitempty on Workflow and the null field appears in JSON.
func TestCreateChainRequestWorkflowOmittedWhenEmpty(t *testing.T) {
	req := filledCreateChainRequest()
	req.Workflow = nil
	req.ClientActorIDs = nil
	got, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	const preChangeGolden = `{
  "name": "test-chain",
  "repo_id": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "base_commit": "1111222233334444555566667777888899990000",
  "plans": [
    "# Plan one\n",
    "# Plan two\n"
  ],
  "settings": {
    "max_corrections": 3,
    "reviewer_actor": "reviewer",
    "planner_actor": "planner",
    "security_actor": "security",
    "security": true,
    "gate": "make check",
    "regate": 2
  },
  "feature": "auth",
  "ticket": "o/r#607",
  "author": {
    "name": "Test Builder",
    "email": "builder@example.com"
  },
  "client_installation": "01CLIENTINSTALLATION0000000",
  "client_binding_ids": {
    "builder": "01CLIENTBINDINGBUILDER000000",
    "reviewer": "01CLIENTBINDINGREVIEWER00000"
  }
}
`
	if !bytes.Equal(got, []byte(preChangeGolden)) {
		t.Fatalf("mismatch:\n--- got ---\n%s\n--- want ---\n%s", string(got), preChangeGolden)
	}
}

func TestContractProtoTypesComplete(t *testing.T) {
	fset := token.NewFileSet()

	var exportedStructs []string
	for _, file := range []string{"proto.go", "chain.go"} {
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", file, err)
		}
		for _, decl := range node.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if !ast.IsExported(ts.Name.Name) {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); ok {
					exportedStructs = append(exportedStructs, ts.Name.Name)
				}
			}
		}
	}

	pinnedNames := make([]string, len(protoCases))
	for i, c := range protoCases {
		pinnedNames[i] = c.name
	}

	sort.Strings(exportedStructs)
	sort.Strings(pinnedNames)

	if !reflect.DeepEqual(exportedStructs, pinnedNames) {
		t.Fatalf("wire exported struct types do not match pinned types in contract_test.go:\nfound in proto.go and chain.go: %v\npinned in test:   %v", exportedStructs, pinnedNames)
	}
}

func TestContractSignedRequests(t *testing.T) {
	fixedKp := fixedKeypair()
	ts := strconv.FormatInt(fixedTime.Unix(), 10)

	canonGet := Canonical(testAudience, "GET", "/v1/whoami", ts, fixedNonce, nil)
	canonGet = append(canonGet, '\n')
	assertGolden(t, "canonical-get", canonGet)

	body := []byte(`{"name":"test-binding"}`)
	bodySum := sha256.Sum256(body)
	canonPost := Canonical(testAudience, "POST", "/v1/bindings", ts, fixedNonce, bodySum[:])
	canonPost = append(canonPost, '\n')
	assertGolden(t, "canonical-post", canonPost)

	hdr := Sign(fixedKp, testAudience, "POST", "/v1/bindings", bodySum[:], fixedTime, fixedNonce)

	var sortedHeaders strings.Builder
	var keys []string
	for k := range hdr {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := slices.Clone(hdr[k])
		sort.Strings(vals)
		for _, v := range vals {
			fmt.Fprintf(&sortedHeaders, "%s: %s\n", k, v)
		}
	}
	assertGolden(t, "sign-headers", []byte(sortedHeaders.String()))

	lookup := func(id ClientID) (ed25519.PublicKey, KeyStatus) {
		if id == IDOf(fixedKp.Public) {
			return fixedKp.Public, KeyActive
		}
		return nil, KeyUnknown
	}
	nonces := NewNonceWindow(10 * time.Minute)

	gotID, err := Verify(hdr, "POST", "/v1/bindings", bodySum[:], fixedTime, lookup, nonces, []string{testAudience})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if gotID != IDOf(fixedKp.Public) {
		t.Fatalf("Verify returned id %q, want %q", gotID, IDOf(fixedKp.Public))
	}

	for _, k := range keys {
		clone := hdr.Clone()
		clone.Del(k)
		testNonces := NewNonceWindow(10 * time.Minute)
		if _, err := Verify(clone, "POST", "/v1/bindings", bodySum[:], fixedTime, lookup, testNonces, []string{testAudience}); err == nil {
			t.Fatalf("Verify unexpectedly succeeded with header %s removed", k)
		}
	}
}
