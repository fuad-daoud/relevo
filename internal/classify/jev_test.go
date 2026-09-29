package classify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJudgeRequestEnvelope(t *testing.T) {
	srv, captured := captureServer(t)
	client := newTestClient(t, srv, nil)

	ans, err := client.Judge(context.Background(), twoParagraphRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans.Model != "jev-latest" {
		t.Errorf("model = %q, want %q", ans.Model, "jev-latest")
	}
	if captured.auth != "Bearer k" {
		t.Errorf("Authorization = %q, want %q", captured.auth, "Bearer k")
	}
	if captured.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", captured.contentType, "application/json")
	}
	if captured.body["model"] != "jev-latest" {
		t.Errorf("body model = %v, want jev-latest", captured.body["model"])
	}

	state, ok := captured.body["state"].(map[string]any)
	if !ok {
		t.Fatalf("state missing or not object: %v", captured.body["state"])
	}
	if state["source"] != "report" || state["harness"] != "claude" {
		t.Errorf("state = %v, want source report, harness claude", state)
	}
	paras, ok := state["paragraphs"].([]any)
	if !ok || len(paras) != 2 {
		t.Fatalf("state.paragraphs invalid: %v", state["paragraphs"])
	}
	if p1, ok := paras[1].(map[string]any); !ok || p1["kind"] != "fenced" {
		t.Errorf("paras[1].kind = %v, want fenced", paras[1])
	}
}

func TestJudgeQuestionShape(t *testing.T) {
	srv, captured := captureServer(t)
	client := newTestClient(t, srv, nil)

	if _, err := client.Judge(context.Background(), twoParagraphRequest()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	questions, ok := captured.body["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions missing: %v", captured.body["questions"])
	}
	for _, qid := range []string{"p0", "p1"} {
		q, ok := questions[qid].(map[string]any)
		if !ok || q["type"] != "noul" {
			t.Errorf("questions.%s type = %v, want noul", qid, questions[qid])
		}
	}
	q1, _ := questions["p1"].(map[string]any)
	instr, _ := q1["instructions"].(string)
	if !strings.Contains(instr, "`paragraphs[1].text`") {
		t.Errorf("q1 instructions does not contain `paragraphs[1].text`: %q", instr)
	}
	if crit, ok := q1["criteria"].(map[string]any); !ok || crit["true"] == nil || crit["false"] == nil {
		t.Errorf("q1 criteria missing true or false: %v", q1["criteria"])
	}
}

func TestJudgeAnswers(t *testing.T) {
	cases := []struct {
		name        string
		body        map[string]any
		wantModel   string
		wantTokens  int
		wantProbs   []float64
		wantMissing string
	}{
		{
			name:       "answers are read by id in request order",
			body:       answersBody("jev-model-xyz", 42, 0.1, 0.9),
			wantModel:  "jev-model-xyz",
			wantTokens: 42,
			wantProbs:  []float64{0.1, 0.9},
		},
		{
			name:        "an answer missing for a paragraph is an error naming it",
			body:        answersBody("jev-latest", 0, 0.1),
			wantMissing: "p1",
		},
		{
			name:      "probabilities outside [0,1] clamp",
			body:      answersBody("jev-latest", 0, -0.5, 1.5),
			wantModel: "jev-latest",
			wantProbs: []float64{0, 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, jsonServer(t, tc.body), nil)
			ans, err := client.Judge(context.Background(), twoParagraphRequest())
			if tc.wantMissing != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantMissing) {
					t.Fatalf("expected error mentioning %q, got: %v", tc.wantMissing, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ans.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", ans.Model, tc.wantModel)
			}
			if ans.InputTokens != tc.wantTokens {
				t.Errorf("InputTokens = %d, want %d", ans.InputTokens, tc.wantTokens)
			}
			if len(ans.Probabilities) != len(tc.wantProbs) || ans.Probabilities[0] != tc.wantProbs[0] || ans.Probabilities[1] != tc.wantProbs[1] {
				t.Errorf("Probabilities = %v, want %v", ans.Probabilities, tc.wantProbs)
			}
		})
	}
}

func TestJudgeRetriesOnce(t *testing.T) {
	cases := []struct {
		name   string
		status int
		prob   float64
	}{
		{"429 then success", http.StatusTooManyRequests, 0.5},
		{"529 then success", 529, 0.2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count int32
			var sleeps []time.Duration
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&count, 1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("overloaded"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(answersBody("jev-latest", 0, tc.prob))
			}))
			t.Cleanup(srv.Close)

			client := newTestClient(t, srv, func(d time.Duration) { sleeps = append(sleeps, d) })
			ans, err := client.Judge(context.Background(), proseRequest())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(ans.Probabilities) != 1 || ans.Probabilities[0] != tc.prob {
				t.Errorf("Probabilities = %v, want [%v]", ans.Probabilities, tc.prob)
			}
			if got := atomic.LoadInt32(&count); got != 2 {
				t.Errorf("requestCount = %d, want 2", got)
			}
			if len(sleeps) != 1 {
				t.Errorf("len(sleeps) = %d, want 1", len(sleeps))
			}
		})
	}
}

func TestJudgeStatusErrors(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantErr      error
		wantCode     int
		wantContains string
		wantRequests int32
	}{
		{"429 on every attempt gives up after one retry", http.StatusTooManyRequests, "rate limited", nil, 429, "", 2},
		{"401 is not retried", http.StatusUnauthorized, "unauthorized", ErrUnauthorized, 0, "", 1},
		{"422 is not retried and carries the body", http.StatusUnprocessableEntity, "unprocessable entity details", ErrBadRequest, 0, "unprocessable entity details", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&count, 1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			client := newTestClient(t, srv, func(time.Duration) {})
			_, err := client.Judge(context.Background(), proseRequest())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got: %v", tc.wantErr, err)
			}
			if tc.wantCode != 0 {
				var statusErr *StatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("expected *StatusError, got %T: %v", err, err)
				}
				if statusErr.Code != tc.wantCode {
					t.Errorf("statusErr.Code = %d, want %d", statusErr.Code, tc.wantCode)
				}
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("expected error to contain %q, got: %v", tc.wantContains, err)
			}
			if got := atomic.LoadInt32(&count); got != tc.wantRequests {
				t.Errorf("requestCount = %d, want %d", got, tc.wantRequests)
			}
		})
	}
}

func TestJudgeRetryDoesNotExceedDeadline(t *testing.T) {
	var sleepCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("too many requests"))
	}))
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv, func(time.Duration) { sleepCalled = true })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Judge(ctx, proseRequest())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if sleepCalled {
		t.Error("Sleep was called, expected immediate return without sleep")
	}
}

func TestJudgeEmpty(t *testing.T) {
	var requestCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv, nil)
	_, err := client.Judge(context.Background(), Request{Source: "report"})
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got: %v", err)
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Errorf("requestCount = %d, want 0", got)
	}
}

func TestJudgeContextCancelled(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(block)
		srv.CloseClientConnections()
		srv.Close()
	})

	client := NewClient("k", "jev-latest")
	client.BaseURL = srv.URL
	client.HTTP = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := client.Judge(ctx, proseRequest())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error to wrap context.Canceled, got: %v", err)
	}
}
