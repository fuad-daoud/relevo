package classify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server, sleep func(time.Duration)) *Client {
	t.Helper()
	c := NewClient("k", "jev-latest")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.Sleep = sleep
	return c
}

func proseRequest() Request {
	return Request{
		Source: "report",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "para 0", Line: 1, Lines: 1},
		},
	}
}

func twoParagraphRequest() Request {
	return Request{
		Source:  "report",
		Harness: "claude",
		Paragraphs: []Paragraph{
			{Index: 0, Kind: KindProse, Text: "prose line", Line: 1, Lines: 1},
			{Index: 1, Kind: KindFenced, Text: "fenced line", Line: 3, Lines: 1},
		},
	}
}

func answersBody(model string, inputTokens int, probs ...float64) map[string]any {
	answers := make(map[string]any, len(probs))
	for i, p := range probs {
		answers[fmt.Sprintf("p%d", i)] = map[string]any{"type": "noul", "noul": p}
	}
	return map[string]any{
		"model":   model,
		"answers": answers,
		"usage":   map[string]any{"input_tokens": inputTokens},
	}
}

func jsonServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type capturedRequest struct {
	auth        string
	contentType string
	body        map[string]any
}

func captureServer(t *testing.T) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured.body)
		captured.auth = r.Header.Get("Authorization")
		captured.contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answersBody("jev-latest", 100, 0.1, 0.8))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}
