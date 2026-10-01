package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var jst = time.FixedZone("JST", 9*3600)

func sampleQuery() Query {
	reference := time.Date(2026, 9, 28, 15, 47, 0, 0, jst)
	return Query{
		Reference: reference, WindowStart: reference.Add(-5 * time.Minute), WindowEnd: reference, Minutes: 5,
		Symptom: "The website cannot be reached",
		Logs: []Log{
			{Path: "/var/log/syslog", Text: "2026-09-28T15:46:20.114+09:00 web01 systemd[1]: Reload failed for nginx.service"},
			{Path: "/var/log/nginx/error.log", Text: `2026-09-28T15:46:20.000+09:00 [emerg] unknown directive "proxy_passs"`},
		},
	}
}

// JV-01: one request, one Score question per log, all logs in the state.
func TestBuildRequest(t *testing.T) {
	request := BuildRequest(sampleQuery())
	if request.Model != "jev-latest" || len(request.Questions) != 2 || len(request.State.Logs) != 2 {
		t.Fatalf("unexpected request: %+v", request)
	}
	question := request.Questions["log2"]
	if question.Type != "score" || len(question.Criteria) != 4 || !strings.Contains(question.Instructions, "/var/log/nginx/error.log") {
		t.Errorf("unexpected question: %+v", question)
	}
	if request.State.IncidentTime != "2026-09-28T15:47:00+09:00" || request.State.Symptom == "" {
		t.Errorf("unexpected state: %+v", request.State)
	}
}

func testClient(url string) *Client {
	client := NewClient("test-key")
	client.endpoint = url
	client.Sleep = func(time.Duration) {}
	return client
}

func answer(scores ...float64) string {
	answers := map[string]any{}
	for i, score := range scores {
		answers[questionName(i)] = map[string]any{"type": "score", "score": score, "confidence": 0.9, "legend": map[string]string{}, "probabilities": map[string]float64{}}
	}
	body, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]int{"input_tokens": 1234, "output_tokens": 5}})
	return string(body)
}

func TestAskParsesScores(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing key")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"type":"score"`) {
			t.Errorf("unexpected body %s", body)
		}
		io.WriteString(w, answer(2.1, 2.97))
	}))
	defer server.Close()
	result, err := testClient(server.URL).Ask(context.Background(), sampleQuery())
	if err != nil {
		t.Fatal(err)
	}
	ranked := Ranked(result.Scores)
	if ranked[0].Path != "/var/log/nginx/error.log" || ranked[0].Priority < 0.98 || result.InputTokens != 1234 {
		t.Errorf("unexpected result %+v", result)
	}
}

// JV-02: which failures are retried once and which are not.
func TestRetryPolicy(t *testing.T) {
	cases := []struct {
		name     string
		statuses []int
		want     error
		attempts int32
	}{
		{"429 then ok", []int{429, 200}, nil, 2},
		{"503 twice", []int{503, 503}, ErrUnavailable, 2},
		{"401 not retried", []int{401}, ErrAuth, 1},
		{"422 not retried", []int{422}, ErrRejected, 1},
	}
	for _, c := range cases {
		var attempts int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&attempts, 1)
			status := c.statuses[len(c.statuses)-1]
			if int(n) <= len(c.statuses) {
				status = c.statuses[n-1]
			}
			w.WriteHeader(status)
			if status == 200 {
				io.WriteString(w, answer(1, 3))
			} else {
				io.WriteString(w, `{"detail":"x"}`)
			}
		}))
		_, err := testClient(server.URL).Ask(context.Background(), sampleQuery())
		server.Close()
		if c.want == nil && err != nil || c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if attempts != c.attempts {
			t.Errorf("%s: %d attempts, want %d", c.name, attempts, c.attempts)
		}
	}
}

func TestUnreachableIsRetriedThenUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing listens any more
	_, err := testClient(url).Ask(context.Background(), sampleQuery())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestMissingScoreIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, answer(2)) // only log1
	}))
	defer server.Close()
	if _, err := testClient(server.URL).Ask(context.Background(), sampleQuery()); !errors.Is(err, ErrRejected) {
		t.Errorf("err = %v, want ErrRejected", err)
	}
}

func TestRankedKeepsOrderForTies(t *testing.T) {
	ranked := Ranked([]Score{{Path: "a", Priority: 0.5}, {Path: "b", Priority: 0.9}, {Path: "c", Priority: 0.5}})
	if ranked[0].Path != "b" || ranked[1].Path != "a" || ranked[2].Path != "c" {
		t.Errorf("got %v", ranked)
	}
}

// SEC-006: a redirect must not resend the log lines, or the API key, to
// wherever it points.
func TestRedirectIsNotFollowed(t *testing.T) {
	var elsewhere struct {
		hits int
		auth string
		body string
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.hits++
		elsewhere.auth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		elsewhere.body = string(data)
		w.Write([]byte(answer(2.7)))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := testClient(server.URL)
	_, err := client.Ask(context.Background(), Query{Logs: []Log{{Path: "/var/log/messages", Text: "secret log line"}}})
	if err == nil {
		t.Error("the redirect was accepted as an answer")
	}
	if elsewhere.hits != 0 {
		t.Errorf("the redirect was followed %d times: auth=%q body=%q", elsewhere.hits, elsewhere.auth, elsewhere.body)
	}
}

// An error body comes from the network and is written to the terminal, so it
// must not carry escape sequences (SEC-005).
func TestSummarizeEscapesControlCharacters(t *testing.T) {
	got := summarize([]byte("bad \x1b[2J request"))
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequence kept: %q", got)
	}
}
