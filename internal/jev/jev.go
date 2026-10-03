// Package jev asks Jev to score each log by how worth it is to examine
// first, in one request with one Score question per log (spec O-01).
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/takeshiue/jevtri/internal/printsafe"
	"sort"
	"strconv"
	"time"
)

// Endpoint is fixed so that logs are never sent to an unintended host.
const Endpoint = "https://api.typesafe.ai/v1/systemone"

// Model is the model alias requested.
const Model = "jev-latest"

const (
	requestTimeout  = 10 * time.Second
	maxResponseSize = 1 << 20
)

// Task and levels were validated in the 2026-09-28 experiment
// (docs/experiments/jev-question-2026-09-28-results.md).
const task = "These are log excerpts collected from one Linux server around the time of an incident. " +
	"Decide which log an engineer should examine in detail first to find the cause."

// groupTask is the first stage of the ranking by group (spec 12.7.4): each
// entry of logs is a group of logs, such as the containers of one service.
const groupTask = "These are log excerpts collected from one Linux server around the time of an incident, " +
	"put together by group: each group holds the logs of one service or of the host, and each excerpt starts with the log's name. " +
	"Decide which group an engineer should examine in detail first to find the cause."

var scoreLevels = []string{
	"Nothing related to the incident; can be skipped",
	"Only indirect or routine information",
	"Shows symptoms or effects of the incident",
	"Shows the cause of the incident or its direct evidence; examine this first",
}

// Log is one log to score.
type Log struct {
	Path string
	Text string
}

// Query is everything sent in one request.
type Query struct {
	Reference   time.Time
	WindowStart time.Time
	WindowEnd   time.Time
	Minutes     int
	Symptom     string
	Logs        []Log
	// Groups is set for the first stage: each Log is a group, Path its name.
	Groups bool
}

// Score is the result for one log, in the order of Query.Logs.
type Score struct {
	Path       string
	Raw        float64 // expected level, 0..3
	Priority   float64 // Raw scaled to 0..1
	Confidence float64
}

// Result is the parsed answer.
type Result struct {
	Model       string
	Scores      []Score
	InputTokens int
	Latency     time.Duration
}

// Request is the JSON body. It is exported so that --dry-run and the send
// log can show exactly what is sent.
type Request struct {
	Model     string              `json:"model"`
	State     State               `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// State is the content the questions refer to.
type State struct {
	Task          string            `json:"task"`
	IncidentTime  string            `json:"incident_time"`
	WindowStart   string            `json:"window_start"`
	WindowEnd     string            `json:"window_end"`
	WindowMinutes int               `json:"window_minutes"`
	Symptom       string            `json:"symptom,omitempty"`
	Logs          map[string]string `json:"logs"`
}

// Question is a Score question.
type Question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

// BuildRequest turns a query into the request body. Question names are
// log1..logN because paths contain characters unsuitable as names.
func BuildRequest(q Query) Request {
	request := Request{
		Model: Model,
		State: State{
			Task:          taskOf(q),
			IncidentTime:  q.Reference.Format(time.RFC3339),
			WindowStart:   q.WindowStart.Format(time.RFC3339),
			WindowEnd:     q.WindowEnd.Format(time.RFC3339),
			WindowMinutes: q.Minutes,
			Symptom:       q.Symptom,
			Logs:          map[string]string{},
		},
		Questions: map[string]Question{},
	}
	seen := map[string]bool{}
	duplicate := false
	for _, log := range q.Logs {
		duplicate = duplicate || seen[log.Path]
		seen[log.Path] = true
	}
	for i, log := range q.Logs {
		key := log.Path
		if duplicate {
			// Keep evidence distinct even if a caller omitted validation.
			key = questionName(i) + ": " + log.Path
		}
		request.State.Logs[key] = log.Text
		request.Questions[questionName(i)] = Question{
			Type:         "score",
			Instructions: instructions(q, key),
			Criteria:     scoreLevels,
		}
	}
	return request
}

// ValidateQuery rejects ambiguous sources before either preview or sending.
func ValidateQuery(q Query) error {
	seen := map[string]bool{}
	for _, log := range q.Logs {
		if seen[log.Path] {
			return fmt.Errorf("%w: duplicate log source %q", ErrRejected, log.Path)
		}
		seen[log.Path] = true
	}
	return nil
}

func questionName(i int) string { return "log" + strconv.Itoa(i+1) }

func taskOf(q Query) string {
	if q.Groups {
		return groupTask
	}
	return task
}

func instructions(q Query, path string) string {
	if q.Groups {
		return fmt.Sprintf("How valuable is it to examine the logs of %s first to find the cause of the incident?", path)
	}
	return fmt.Sprintf("How valuable is it to examine %s first to find the cause of the incident?", path)
}

// Failure kinds, used for exit codes and messages.
var (
	ErrAuth        = errors.New("jev rejected the API key")
	ErrRejected    = errors.New("jev rejected the request")
	ErrUnavailable = errors.New("jev could not be reached")
)

// Client sends requests. Endpoint is only replaced in tests.
type Client struct {
	APIKey   string
	HTTP     *http.Client
	endpoint string
	// Sleep is replaced in tests to avoid waiting before the retry.
	Sleep func(time.Duration)
}

// NewClient returns a client for the fixed endpoint.
func NewClient(apiKey string) *Client {
	// No redirects: a 307 would resend the log lines, and the Authorization
	// header within the same host, to wherever it points (SEC-006).
	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{APIKey: apiKey, HTTP: client, endpoint: Endpoint, Sleep: time.Sleep}
}

// Ask sends the query and parses the scores. Network failures, timeouts,
// 429 and 5xx are retried once; other failures are not (spec 12.3).
func (c *Client) Ask(ctx context.Context, q Query) (Result, error) {
	if err := ValidateQuery(q); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(BuildRequest(q))
	if err != nil {
		return Result{}, err
	}
	started := time.Now()
	payload, err := c.send(ctx, body)
	if err != nil {
		return Result{}, err
	}
	result, err := parse(payload, q)
	result.Latency = time.Since(started)
	return result, err
}

var errRetryable = errors.New("retryable")

func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		// Transport failures (refused, reset, DNS, timeout) are all retried once.
		return nil, fmt.Errorf("%w: %v", errRetryable, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", errRetryable, err)
	}
	switch {
	case response.StatusCode == http.StatusOK:
		return payload, nil
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", errRetryable, response.StatusCode)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrAuth, response.StatusCode)
	default:
		return nil, fmt.Errorf("%w (HTTP %d): %s", ErrRejected, response.StatusCode, summarize(payload))
	}
}

// summarize keeps error bodies short in messages.
func summarize(payload []byte) string {
	if len(payload) > 300 {
		return printsafe.Line(string(payload[:300])) + "…"
	}
	return printsafe.Line(string(payload))
}

type response struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type       string   `json:"type"`
		Score      *float64 `json:"score"`
		Confidence float64  `json:"confidence"`
	} `json:"answers"`
	Usage struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
}

func parse(payload []byte, q Query) (Result, error) {
	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Result{}, fmt.Errorf("%w: unreadable response: %v", ErrRejected, err)
	}
	result := Result{Model: decoded.Model, InputTokens: decoded.Usage.InputTokens}
	top := float64(len(scoreLevels) - 1)
	for i, log := range q.Logs {
		answer, ok := decoded.Answers[questionName(i)]
		if !ok || answer.Type != "score" || answer.Score == nil {
			// Never invent a priority for a log that was not scored.
			return Result{}, fmt.Errorf("%w: no score for %s", ErrRejected, log.Path)
		}
		if *answer.Score < 0 || *answer.Score > top {
			return Result{}, fmt.Errorf("%w: score %v out of range for %s", ErrRejected, *answer.Score, log.Path)
		}
		result.Scores = append(result.Scores, Score{Path: log.Path, Raw: *answer.Score, Priority: *answer.Score / top, Confidence: answer.Confidence})
	}
	return result, nil
}

// Ranked returns the scores in descending priority, keeping the input order
// for ties (spec: equal priorities stay in configuration order).
func Ranked(scores []Score) []Score {
	ranked := append([]Score(nil), scores...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Priority > ranked[j].Priority })
	return ranked
}
