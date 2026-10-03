package casereport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

func TestReportOnlyFinalRanking(t *testing.T) {
	groupRequest := jev.BuildRequest(jev.Query{Groups: true, Reference: time.Now(), Logs: []jev.Log{{Path: "shop", Text: "excerpt"}}})
	task := groupRequest.State.Task
	oldGroup := strings.Replace(line("2026-10-03T12:00:00Z"), `"symptom":"s"`, `"task":"`+task+`","symptom":"s"`, 1)
	newGroup := strings.Replace(line("2026-10-03T12:00:01Z"), `"dry_run":false`, `"dry_run":false,"purpose":"group_ranking"`, 1)
	final := strings.Replace(line("2026-10-03T12:00:02Z"), `"dry_run":false`, `"dry_run":false,"purpose":"log_ranking"`, 1)
	path := filepath.Join(t.TempDir(), "sent.log")
	if err := os.WriteFile(path, []byte(oldGroup+newGroup+final+line("2026-10-03T11:00:00Z")), 0600); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadRuns(path)
	if err != nil || len(runs) != 2 {
		t.Fatalf("intermediate groups offered: %d runs, %v", len(runs), err)
	}
	if runs[0].Time.Second() != 2 {
		t.Fatalf("final ranking missing: %+v", runs)
	}
}

func TestRankingSortBeforeRounding(t *testing.T) {
	entry := strings.ReplaceAll(line("2026-10-03T12:00:00Z"), `"score":0.3`, `"score":2.101`)
	entry = strings.ReplaceAll(entry, `"score":2.7`, `"score":2.102`)
	path := filepath.Join(t.TempDir(), "sent.log")
	if err := os.WriteFile(path, []byte(entry), 0600); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadRuns(path)
	if err != nil || len(runs) != 1 {
		t.Fatal(err)
	}
	if runs[0].Rankings[0].Path != "/b" || runs[0].Rankings[0].Priority != 70 {
		t.Fatalf("rounded values reordered: %+v", runs[0].Rankings)
	}
}
