package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

var sampleChoices = []FormatChoice{{Name: "syslog", Example: "Sep 28 07:51:24"}, {Name: "iso-space", Example: "2026-09-28 03:03:07"}}

func TestBuildFormatRequest(t *testing.T) {
	request := BuildFormatRequest("/opt/app/app.log", "line1\nline2", sampleChoices)
	question := request.Questions["time_format"]
	if question.Type != "choice" || len(question.Criteria) != 3 || question.Criteria[NoFormat] == "" {
		t.Fatalf("unexpected question %+v", question)
	}
	if request.State.Lines != "line1\nline2" || request.State.Path != "/opt/app/app.log" {
		t.Errorf("unexpected state %+v", request.State)
	}
}

func formatServer(t *testing.T, probabilities map[string]float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request FormatRequest
		if err := json.Unmarshal(body, &request); err != nil || request.Questions["time_format"].Type != "choice" {
			t.Errorf("unexpected body %s", body)
		}
		answer, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": map[string]any{
			"time_format": map[string]any{"type": "choice", "probabilities": probabilities},
		}})
		w.Write(answer)
	}))
}

// IN-03: the answer is ordered by probability.
func TestAskFormat(t *testing.T) {
	server := formatServer(t, map[string]float64{"syslog": 0.07, "iso-space": 0.9, "none": 0.03})
	defer server.Close()
	result, err := testClient(server.URL).AskFormat(context.Background(), BuildFormatRequest("/x", "l", sampleChoices))
	if err != nil {
		t.Fatal(err)
	}
	if best := result.Best(); best.Name != "iso-space" || best.Probability != 0.9 || len(result.Options) != 3 {
		t.Errorf("unexpected %+v", result)
	}
}

// An option that was not offered means the answer cannot be trusted.
func TestAskFormatUnknownOption(t *testing.T) {
	server := formatServer(t, map[string]float64{"rfc3339": 1})
	defer server.Close()
	_, err := testClient(server.URL).AskFormat(context.Background(), BuildFormatRequest("/x", "l", sampleChoices))
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("got %v", err)
	}
}
