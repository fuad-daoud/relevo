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
		ID:            ClientID("SHA256:fixed-test-client-id-00000000000000000000000"),
		Label:         "test-client",
		ServerVersion: 1,
		Transports:    []string{"git-bundle"},
		Features:      []string{"tier", "queue", "stop"},
		BuilderTier:   "edit",
		MaxTier:       "yolo",
		Builders:      &builders,
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
		Name:           "test-binding",
		RepoID:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BaseCommit:     "1111222233334444555566667777888899990000",
		Candidate:      "agy/openai/gpt-4",
		RoundCap:       10,
		RoundTimeoutMS: 60000,
		Tier:           "edit",
		Role:           "builder",
		Feature:        "auth",
		Ticket:         "o/r#607",
		Author:         &author,
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
		Running: 2,
		Queued:  1,
		Cap:     4,
		Scopes:  true,
		Slice:   "relevo.slice",
		Quota:   "200%",
	}
}

func filledBindingView() BindingView {
	u := filledUsage()
	rusage := store.Rusage{CPUMS: 2500, PeakMemBytes: 52428800}
	prior := usage.Tokens{In: 50, CacheRead: 100, CacheWrite: 20, Out: 10}
	q := filledQueueView()
	live := filledLiveView()
	return BindingView{
		Name:           "test-binding",
		State:          "active",
		Round:          2,
		RoundState:     RoundRunning,
		ClosedRound:    1,
		Halt:           "user-requested halt",
		ResultCommit:   "3333444455556666777788889999000011112222",
		DirtyCommit:    "4444555566667777888899990000111122223333",
		ReportOutcome:  "completed",
		Stopped:        "killed",
		DiffNote:       "refactored remote wire",
		DiffCommits:    2,
		DiffTree:       "5555666677778888999900001111222233334444",
		AckedRound:     1,
		Candidate:      "agy/openai/gpt-4",
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

func filledErrorBody() ErrorBody {
	return ErrorBody{
		Code:    CodeNotEnrolled,
		Message: "unknown client key",
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

func TestContractProtoTypesComplete(t *testing.T) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "proto.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile proto.go: %v", err)
	}

	var exportedStructs []string
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

	pinnedNames := make([]string, len(protoCases))
	for i, c := range protoCases {
		pinnedNames[i] = c.name
	}

	sort.Strings(exportedStructs)
	sort.Strings(pinnedNames)

	if !reflect.DeepEqual(exportedStructs, pinnedNames) {
		t.Fatalf("proto.go exported struct types do not match pinned types in contract_test.go:\nfound in proto.go: %v\npinned in test:   %v", exportedStructs, pinnedNames)
	}
}

func TestContractSignedRequests(t *testing.T) {
	fixedKp := fixedKeypair()
	ts := strconv.FormatInt(fixedTime.Unix(), 10)

	canonGet := Canonical("GET", "/v1/whoami", ts, fixedNonce, nil)
	canonGet = append(canonGet, '\n')
	assertGolden(t, "canonical-get", canonGet)

	body := []byte(`{"name":"test-binding"}`)
	bodySum := sha256.Sum256(body)
	canonPost := Canonical("POST", "/v1/bindings", ts, fixedNonce, bodySum[:])
	canonPost = append(canonPost, '\n')
	assertGolden(t, "canonical-post", canonPost)

	hdr := Sign(fixedKp, "POST", "/v1/bindings", bodySum[:], fixedTime, fixedNonce)

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

	gotID, err := Verify(hdr, "POST", "/v1/bindings", bodySum[:], fixedTime, lookup, nonces)
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
		if _, err := Verify(clone, "POST", "/v1/bindings", bodySum[:], fixedTime, lookup, testNonces); err == nil {
			t.Fatalf("Verify unexpectedly succeeded with header %s removed", k)
		}
	}
}
