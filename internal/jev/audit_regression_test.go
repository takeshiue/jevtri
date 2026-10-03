package jev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScoreMustBePresentAndNonNull(t *testing.T) {
	query := sampleQuery()
	query.Logs = query.Logs[:1]
	for _, fields := range []string{`"type":"score"`, `"type":"score","score":null`} {
		if _, err := parse([]byte(`{"answers":{"log1":{`+fields+`}}}`), query); !errors.Is(err, ErrRejected) {
			t.Errorf("fields %s: expected rejection, got %v", fields, err)
		}
	}
	result, err := parse([]byte(`{"answers":{"log1":{"type":"score","score":0}}}`), query)
	if err != nil || len(result.Scores) != 1 || result.Scores[0].Raw != 0 {
		t.Fatalf("explicit zero must remain valid: %+v %v", result, err)
	}
}

func TestDuplicateSourcesNeverOverwriteEvidenceOrReachServer(t *testing.T) {
	query := sampleQuery()
	query.Logs[1].Path = query.Logs[0].Path
	request := BuildRequest(query)
	if len(request.State.Logs) != 2 || len(request.Questions) != 2 {
		t.Fatalf("evidence overwritten: %+v", request)
	}
	for i, log := range query.Logs {
		key := questionName(i) + ": " + log.Path
		if request.State.Logs[key] != log.Text || !strings.Contains(request.Questions[questionName(i)].Instructions, key) {
			t.Errorf("question and evidence disagree: %+v", request)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("ambiguous source was sent")
	}))
	defer server.Close()
	if _, err := testClient(server.URL).Ask(context.Background(), query); !errors.Is(err, ErrRejected) {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
}
