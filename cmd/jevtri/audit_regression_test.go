package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/budget"
	"github.com/takeshiue/jevtri/internal/grouprank"
	"github.com/takeshiue/jevtri/internal/jev"
	"github.com/takeshiue/jevtri/internal/report"
	"github.com/takeshiue/jevtri/internal/window"
)

func TestAuditGroupStateBudget(t *testing.T) {
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	plan, _ := grouprank.Make([]grouprank.Member{{Name: "a", Groups: []string{"a"}}, {Name: "b", Groups: []string{"b"}}}, nil)
	logs := []budget.Log{{Name: "a", Entries: []budget.Entry{{Time: now, Text: strings.Repeat("\"\\\n日本語", 200)}}}, {Name: "b", Entries: []budget.Entry{{Time: now, Text: strings.Repeat("b", 600)}}}}
	infos := []report.LogInfo{{Name: "a", Path: "/var/log/a"}, {Name: "b", Path: "/var/log/b"}}
	query, _, err := groupQuery(plan, logs, infos, 1500, window.Window{Reference: now}, 5, "symptom <>&")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(jev.BuildRequest(query).State)
	if len(state) > 1500 || len(query.Logs) == 0 {
		t.Fatalf("state bytes=%d query=%+v", len(state), query)
	}
	for _, log := range query.Logs {
		if log.Text == "" {
			t.Fatal("empty group scored")
		}
	}
	if _, _, err := groupQuery(plan, logs, infos, 10, window.Window{Reference: now}, 5, ""); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata: %v", err)
	}
}

func TestAuditEmptyGroupsAreNotScored(t *testing.T) {
	now := time.Now()
	var members []grouprank.Member
	var logs []budget.Log
	var infos []report.LogInfo
	for i := 0; i < 6; i++ {
		name := fmt.Sprint(i)
		members = append(members, grouprank.Member{Name: name, Groups: []string{name}})
		logs = append(logs, budget.Log{Name: name, Entries: []budget.Entry{{Time: now, Text: strings.Repeat("ERROR", 200)}}})
		infos = append(infos, report.LogInfo{Name: name, Path: "/var/log/" + name})
	}
	plan, _ := grouprank.Make(members, nil)
	if query, _, err := groupQuery(plan, logs, infos, 1000, window.Window{Reference: now}, 5, ""); err == nil || len(query.Logs) != 0 {
		t.Fatalf("empty group query=%+v err=%v", query, err)
	}
	logs[0].Entries[0].Text = "ERROR small"
	query, _, err := groupQuery(plan, logs, infos, 1000, window.Window{Reference: now}, 5, "")
	if err != nil || len(query.Logs) != 1 || query.Logs[0].Path != "group 0" {
		t.Fatalf("query=%+v err=%v", query, err)
	}
}

type auditRecordFailureAsker struct {
	path  string
	calls int
	t     *testing.T
}

func (fake *auditRecordFailureAsker) Ask(_ context.Context, query jev.Query) (jev.Result, error) {
	fake.calls++
	if fake.calls == 1 {
		if err := os.Remove(fake.path); err != nil {
			fake.t.Fatal(err)
		}
		if err := os.Mkdir(fake.path, 0700); err != nil {
			fake.t.Fatal(err)
		}
	}
	var scores []jev.Score
	for _, log := range query.Logs {
		scores = append(scores, jev.Score{Path: log.Path, Raw: 3, Priority: 1})
	}
	return jev.Result{Scores: scores}, nil
}

func (fake *auditRecordFailureAsker) AskFormat(context.Context, jev.FormatRequest) (jev.FormatResult, error) {
	return jev.FormatResult{}, fmt.Errorf("unused")
}

func TestAuditFirstStageRecordFailureStopsSending(t *testing.T) {
	f := newFixture(t, "")
	content, _ := os.ReadFile(f.conf)
	content = []byte(strings.ReplaceAll(strings.ReplaceAll(string(content), "[log nginx]", "[log nginx]\ngroup = a"), "[log httpd]", "[log httpd]\ngroup = b"))
	if err := os.WriteFile(f.conf, content, 0600); err != nil {
		t.Fatal(err)
	}
	fake := &auditRecordFailureAsker{path: filepath.Join(f.dir, "state", "sent.log"), t: t}
	env := environment{stdout: &f.stdout, stderr: &f.stderr, now: func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, time.Local) }, newAsker: func(string) asker { return fake }, keyPath: f.key, isTerminal: func() bool { return false }}
	code := run([]string{"-c", f.conf, "-t", "03:02"}, env)
	if code != exitFailure || fake.calls != 1 || !strings.Contains(f.stderr.String(), "no further requests") {
		t.Fatalf("code=%d calls=%d stderr=%s", code, fake.calls, f.stderr.String())
	}
}

func TestAuditQuietAndUnknownGroups(t *testing.T) {
	for _, allQuiet := range []bool{false, true} {
		t.Run(fmt.Sprint(allQuiet), func(t *testing.T) {
			f := newFixture(t, "")
			quiet := filepath.Join(f.dir, "quiet.log")
			if err := os.WriteFile(quiet, nil, 0600); err != nil {
				t.Fatal(err)
			}
			content, _ := os.ReadFile(f.conf)
			if allQuiet {
				content = []byte("[general]\nsent_log = " + filepath.Join(f.dir, "state", "sent.log") + "\n")
			} else {
				content = []byte(strings.ReplaceAll(string(content), "time_format = ", "group = active\ntime_format = "))
			}
			content = append(content, []byte(fmt.Sprintf("\n[log quiet]\npath = %s\ntime_format = iso-space\ngroup = quiet\n", quiet))...)
			if err := os.WriteFile(f.conf, content, 0600); err != nil {
				t.Fatal(err)
			}
			if code := f.run("-t", "03:02", "--group", "quiet", "--dry-run"); code != exitOK || f.asker.query.Logs != nil {
				t.Fatalf("quiet: code=%d stderr=%s", code, f.stderr.String())
			}
			f.stdout.Reset()
			f.stderr.Reset()
			if code := f.run("-t", "03:02", "--group", "unknown", "--dry-run"); code != exitUsage {
				t.Fatalf("unknown: code=%d stderr=%s", code, f.stderr.String())
			}
		})
	}
}

func TestAuditFlatStateBudgetAndMetadataFailure(t *testing.T) {
	f := newFixture(t, "")
	content, _ := os.ReadFile(f.conf)
	if err := os.WriteFile(f.conf, []byte(strings.Replace(string(content), "[general]", "[general]\nmax_bytes = 1600", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("-t", "03:02", "--dry-run", "-i", strings.Repeat("<>&", 30)); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	var request jev.Request
	if err := json.Unmarshal(f.stdout.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(request.State)
	if len(state) > 1600 {
		t.Fatalf("state=%d", len(state))
	}
	f.stdout.Reset()
	f.stderr.Reset()
	if code := f.run("-t", "03:02", "-i", strings.Repeat("x", 2000)); code != exitFailure || len(f.asker.query.Logs) > 0 {
		t.Fatalf("metadata: code=%d stderr=%s", code, f.stderr.String())
	}
}

func TestAuditDuplicateSourcesRejectedBeforePreviewOrSend(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			f := newFixture(t, "")
			content, err := os.ReadFile(f.conf)
			if err != nil {
				t.Fatal(err)
			}
			content = append(content, []byte(fmt.Sprintf("\n[log copy]\npath = %s\ntime_format = iso-space\n", filepath.Join(f.dir, "app.log")))...)
			if err := os.WriteFile(f.conf, content, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-t", "03:02"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			if code := f.run(args...); code != exitFailure || f.stdout.Len() != 0 || len(f.asker.query.Logs) != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, f.stdout.String(), f.stderr.String())
			}
		})
	}
}

func TestAuditFirstStageTransmissionIsDistinguished(t *testing.T) {
	f := newFixture(t, "")
	content, err := os.ReadFile(f.conf)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.ReplaceAll(strings.ReplaceAll(string(content), "[log nginx]", "[log nginx]\ngroup = shop"), "[log httpd]", "[log httpd]\ngroup = blog"))
	if err := os.WriteFile(f.conf, content, 0600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGroupAsker{}
	env := environment{stdout: &f.stdout, stderr: &f.stderr, now: func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, time.Local) }, newAsker: func(string) asker { return fake }, keyPath: f.key, isTerminal: func() bool { return false }}
	if code := run([]string{"-c", f.conf, "-t", "03:02", "-v"}, env); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	if len(fake.queries) != 2 {
		t.Fatalf("calls=%d", len(fake.queries))
	}
	if strings.Contains(f.stdout.String(), "not examined (not sent") || !strings.Contains(f.stdout.String(), "group ranking: sent=7") || !strings.Contains(f.stdout.String(), "excerpt sent for group ranking only") {
		t.Fatalf("output=%s", f.stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "state", "sent.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("records=%d", len(lines))
	}
	var first, second struct{ Purpose string }
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first.Purpose != "group_ranking" || second.Purpose != "" {
		t.Fatalf("purpose=%q,%q", first.Purpose, second.Purpose)
	}
}

func TestAuditLegacyProjectStopsBeforeCollection(t *testing.T) {
	f := newFixture(t, "")
	root := t.TempDir()
	directory := filepath.Join(root, "var", "lib", "docker", "containers", "a1")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"config.v2.json":  `{"ID":"a1","Name":"/api","LogPath":"/var/lib/docker/containers/a1/a1-json.log","Config":{"Labels":{"com.docker.compose.project":"shop"}}}`,
		"hostconfig.json": `{"LogConfig":{"Type":"json-file"}}`,
		"a1-json.log":     `{"log":"ERROR CONTAINER_ONLY\n","stream":"stderr","time":"2026-09-27T18:02:30Z"}` + "\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	conf := fmt.Sprintf("[general]\nsent_log = %s\n\n[log shop]\ndocker_project = shop\n\n[log shop/api]\npath = %s\ntime_format = iso-space\ngroup = host\n", filepath.Join(f.dir, "sent.log"), filepath.Join(f.dir, "app.log"))
	if err := os.WriteFile(f.conf, []byte(conf), 0600); err != nil {
		t.Fatal(err)
	}
	env := environment{stdout: &f.stdout, stderr: &f.stderr, now: func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, time.Local) }, newAsker: func(string) asker { return f.asker }, keyPath: f.key, isTerminal: func() bool { return false }, root: root}
	if code := run([]string{"-c", f.conf, "-t", "03:02", "--group", "host", "--dry-run"}, env); code != exitFailure || len(f.asker.query.Logs) > 0 || f.stdout.Len() != 0 || !strings.Contains(f.stderr.String(), "has no registered Docker log path") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, f.stdout.String(), f.stderr.String())
	}
}
