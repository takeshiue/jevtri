package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// NoFormat is the choice meaning that no catalog format fits.
const NoFormat = "none"

const formatTask = "These are the first lines of one log file on a Linux server. " +
	"Identify how the timestamp at the start of each log entry is written."

// FormatChoice is one catalog format offered to Jev, with an example so the
// model compares shapes rather than names.
type FormatChoice struct {
	Name    string
	Example string
}

// FormatRequest is the JSON body of a time format question (spec O-04:
// masked first lines and the catalog, including "none", as one Choice).
type FormatRequest struct {
	Model     string                    `json:"model"`
	State     FormatState               `json:"state"`
	Questions map[string]ChoiceQuestion `json:"questions"`
}

// FormatState is the content the question refers to.
type FormatState struct {
	Task  string `json:"task"`
	Path  string `json:"path"`
	Lines string `json:"lines"`
}

// ChoiceQuestion is a Choice question; criteria maps each option to its
// description.
type ChoiceQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

const formatQuestion = "time_format"

// BuildFormatRequest makes the request for one log. lines must already be
// masked.
func BuildFormatRequest(path, lines string, choices []FormatChoice) FormatRequest {
	criteria := map[string]string{NoFormat: "None of the other options; or the lines carry no timestamp"}
	for _, c := range choices {
		criteria[c.Name] = "Timestamps written like " + c.Example
	}
	return FormatRequest{
		Model: Model,
		State: FormatState{Task: formatTask, Path: path, Lines: lines},
		Questions: map[string]ChoiceQuestion{formatQuestion: {
			Type:         "choice",
			Instructions: "Which option matches how the timestamps in these lines are written?",
			Criteria:     criteria,
		}},
	}
}

// Option is one answer option with its probability.
type Option struct {
	Name        string
	Probability float64
}

// FormatResult is the parsed answer, most probable option first.
type FormatResult struct {
	Model   string
	Options []Option
	Latency time.Duration
}

// Best returns the most probable option.
func (r FormatResult) Best() Option {
	if len(r.Options) == 0 {
		return Option{Name: NoFormat}
	}
	return r.Options[0]
}

// AskFormat sends one time format question, with the same retry rules as
// Ask.
func (c *Client) AskFormat(ctx context.Context, request FormatRequest) (FormatResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return FormatResult{}, err
	}
	started := time.Now()
	payload, err := c.send(ctx, body)
	if err != nil {
		return FormatResult{}, err
	}
	result, err := parseFormat(payload, request)
	result.Latency = time.Since(started)
	return result, err
}

func parseFormat(payload []byte, request FormatRequest) (FormatResult, error) {
	var decoded struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string             `json:"type"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return FormatResult{}, fmt.Errorf("%w: unreadable response: %v", ErrRejected, err)
	}
	answer, ok := decoded.Answers[formatQuestion]
	if !ok || answer.Type != "choice" || len(answer.Probabilities) == 0 {
		return FormatResult{}, fmt.Errorf("%w: no answer to the time format question", ErrRejected)
	}
	offered := request.Questions[formatQuestion].Criteria
	result := FormatResult{Model: decoded.Model}
	for name, probability := range answer.Probabilities {
		if _, ok := offered[name]; !ok {
			return FormatResult{}, fmt.Errorf("%w: answer %q was not offered", ErrRejected, name)
		}
		result.Options = append(result.Options, Option{Name: name, Probability: probability})
	}
	sort.SliceStable(result.Options, func(i, j int) bool {
		if result.Options[i].Probability != result.Options[j].Probability {
			return result.Options[i].Probability > result.Options[j].Probability
		}
		return result.Options[i].Name < result.Options[j].Name
	})
	return result, nil
}

// send posts body, retrying once on network failures, timeouts, 429 and
// 5xx (spec 12.3).
func (c *Client) send(ctx context.Context, body []byte) ([]byte, error) {
	payload, err := c.post(ctx, body)
	if errors.Is(err, errRetryable) {
		c.Sleep(time.Second)
		payload, err = c.post(ctx, body)
	}
	if errors.Is(err, errRetryable) {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return payload, err
}
