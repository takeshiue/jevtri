package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// Stored paths must remain readable without Docker discovery.
func TestDockerAndJournalSources(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var", "lib", "docker", "containers", "abc123")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(`{"ID":"abc123","Name":"/web","LogPath":"/var/lib/docker/containers/abc123/abc123-json.log"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "hostconfig.json"), []byte(`{"LogConfig":{"Type":"json-file"}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "abc123-json.log"), []byte(
		`{"log":"GET /health 200\n","stream":"stdout","time":"2026-09-27T17:50:00.000000001Z"}`+"\n"+
			`{"log":"ERROR db: connection refused\n","stream":"stderr","time":"2026-09-27T18:02:30.123456789Z"}`+"\n"), 0o640)
	os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "usr", "bin", "journalctl"), []byte(
		"#!/bin/sh\necho '2026-09-28T03:01:00.000001+09:00 host dockerd[42]: level=error msg=\"container web exited\"'\n"), 0o755)

	f := newFixture(t, fmt.Sprintf("\n[log web]\npath = %s\ndocker_container = web\ngroup = web\n\n[log dockerd]\njournal_unit = docker.service\ngroup = system\n\n[log gone]\npath = %s\ndocker_container = gone\ngroup = web\n", filepath.Join(dir, "abc123-json.log"), filepath.Join(dir, "missing-json.log")))
	jst := time.FixedZone("JST", 9*3600)
	env := environment{
		stdout: &f.stdout, stderr: &f.stderr,
		now:        func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) },
		newAsker:   func(string) asker { return f.asker },
		keyPath:    f.key,
		isTerminal: func() bool { return false },
		getenv:     func(string) string { return "" },
		root:       root,
	}
	code := run([]string{"-c", f.conf, "-t", "03:02", "-m", "5"}, env)
	if code != exitPartial {
		t.Fatalf("exit %d, stderr: %s", code, f.stderr.String())
	}
	sent := map[string]string{}
	for _, log := range f.asker.query.Logs {
		sent[log.Path] = log.Text
	}
	if !strings.Contains(sent["docker:web"], `connection refused\n","stream":"stderr","time":"2026-09-28T03:02:30.123+09:00"`) || strings.Contains(sent["docker:web"], "/health") {
		t.Errorf("docker log sent: %q", sent["docker:web"])
	}
	if !strings.Contains(sent["journal:docker.service"], "container web exited") {
		t.Errorf("journal sent: %q", sent["journal:docker.service"])
	}
	if !strings.Contains(f.stderr.String(), "Log file not found:") || !strings.Contains(f.stderr.String(), "missing-json.log") {
		t.Errorf("missing container not reported: %s", f.stderr.String())
	}
}

// IN-07: --config-update needs a terminal and takes no other argument.
func TestConfigUpdateOption(t *testing.T) {
	f := newFixture(t, "")
	if code := f.run("--config-update"); code != exitFailure || !strings.Contains(f.stderr.String(), "needs a terminal") {
		t.Errorf("without terminal: %d %s", code, f.stderr.String())
	}
	f.stderr.Reset()
	if code := f.run("--config-update", "init"); code != exitUsage {
		t.Errorf("extra argument: %d", code)
	}
}

// A saved project must not expand to unregistered members during a run.
func TestDockerProjectRun(t *testing.T) {
	root := t.TempDir()
	write := func(id, name, project, line string) {
		dir := filepath.Join(root, "var", "lib", "docker", "containers", id)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(`{"ID":"`+id+`","Name":"/`+name+`","LogPath":"/var/lib/docker/containers/`+id+`/`+id+`-json.log","Config":{"Labels":{"com.docker.compose.project":"`+project+`"}}}`), 0o600)
		os.WriteFile(filepath.Join(dir, "hostconfig.json"), []byte(`{"LogConfig":{"Type":"json-file"}}`), 0o600)
		os.WriteFile(filepath.Join(dir, id+"-json.log"), []byte(line+"\n"), 0o640)
	}
	write("a1", "shop-api-1", "shop", `{"log":"ERROR payment failed\n","stream":"stderr","time":"2026-09-27T18:02:30Z"}`)
	write("b2", "shop-db-1", "shop", `{"log":"FATAL disk full\n","stream":"stderr","time":"2026-09-27T18:02:31Z"}`)
	write("c3", "blog-web-1", "blog", `{"log":"GET / 200\n","stream":"stdout","time":"2026-09-27T18:02:32Z"}`)

	f := newFixture(t, fmt.Sprintf("\n[log shop-api]\npath = %s\ndocker_container = shop-api-1\ndocker_project = shop\ngroup = shop\n\n[log shop-db]\npath = %s\ndocker_container = shop-db-1\ndocker_project = shop\ngroup = shop\n", filepath.Join(root, "var/lib/docker/containers/a1/a1-json.log"), filepath.Join(root, "var/lib/docker/containers/b2/b2-json.log")))
	jst := time.FixedZone("JST", 9*3600)
	env := environment{
		stdout: &f.stdout, stderr: &f.stderr,
		now:        func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) },
		newAsker:   func(string) asker { return f.asker },
		keyPath:    f.key,
		isTerminal: func() bool { return false },
		getenv:     func(string) string { return "" },
		root:       root,
	}
	if code := run([]string{"-c", f.conf, "-t", "03:02", "-m", "5"}, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sent := map[string]string{}
	for _, log := range f.asker.query.Logs {
		sent[log.Path] = log.Text
	}
	if !strings.Contains(sent["docker:shop-api-1"], "payment failed") || !strings.Contains(sent["docker:shop-db-1"], "disk full") {
		t.Errorf("project containers not sent: %v", sent)
	}
	if _, ok := sent["docker:blog-web-1"]; ok {
		t.Error("an unregistered project was sent")
	}
	if strings.Contains(f.stderr.String(), "blog") {
		t.Errorf("runtime discovered an unregistered project: %s", f.stderr.String())
	}
}

// fakeGroupAsker answers the first stage with shop first, and records both
// queries.
type fakeGroupAsker struct{ queries []jev.Query }

func (f *fakeGroupAsker) Ask(_ context.Context, q jev.Query) (jev.Result, error) {
	f.queries = append(f.queries, q)
	var scores []jev.Score
	for _, log := range q.Logs {
		raw := 1.0
		if strings.Contains(log.Path, "shop") {
			raw = 2.8
		}
		scores = append(scores, jev.Score{Path: log.Path, Raw: raw, Priority: raw / 3})
	}
	return jev.Result{Model: "jev-test", Scores: scores}, nil
}

func (f *fakeGroupAsker) AskFormat(context.Context, jev.FormatRequest) (jev.FormatResult, error) {
	return jev.FormatResult{}, nil
}

// RK-03: with two service groups the run asks Jev twice: first which group,
// then the logs of that group with the logs that are always examined. With
// --group only the second stage runs.
func TestTwoStageRun(t *testing.T) {
	dir := t.TempDir()
	logFile := func(name, line string) string {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(line+"\n"), 0o644)
		return path
	}
	conf := filepath.Join(dir, "jevtri.conf")
	os.WriteFile(conf, []byte(fmt.Sprintf(`[general]
sent_log = %s

[log sys]
path = %s
time_format = iso-space
group = system

[log shop-api]
path = %s
time_format = iso-space
group = shop

[log blog-web]
path = %s
time_format = iso-space
group = blog
`, filepath.Join(dir, "sent.log"),
		logFile("sys.log", "2026-09-28 03:02:00 kernel: disk ok"),
		logFile("shop.log", "2026-09-28 03:02:10 ERROR payment failed"),
		logFile("blog.log", "2026-09-28 03:02:20 GET / 200"))), 0o644)
	key := filepath.Join(dir, "api-key")
	os.WriteFile(key, []byte("k\n"), 0o600)
	jst := time.FixedZone("JST", 9*3600)
	runWith := func(args ...string) (*fakeGroupAsker, string, string, int) {
		var stdout, stderr bytes.Buffer
		fake := &fakeGroupAsker{}
		env := environment{stdout: &stdout, stderr: &stderr,
			now:      func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) },
			newAsker: func(string) asker { return fake }, keyPath: key,
			isTerminal: func() bool { return false }, getenv: func(string) string { return "" }}
		code := run(append([]string{"-c", conf, "-t", "03:02", "-m", "5"}, args...), env)
		return fake, stdout.String(), stderr.String(), code
	}

	fake, out, errs, code := runWith()
	if code != exitOK || len(fake.queries) != 2 {
		t.Fatalf("exit %d, %d queries: %s", code, len(fake.queries), errs)
	}
	first, second := fake.queries[0], fake.queries[1]
	if !first.Groups || len(first.Logs) != 2 || first.Logs[0].Path != "group blog" || !strings.Contains(first.Logs[1].Text, "payment failed") {
		t.Errorf("first stage: %+v", first)
	}
	var paths []string
	for _, log := range second.Logs {
		paths = append(paths, filepath.Base(log.Path))
	}
	if strings.Join(paths, ",") != "sys.log,shop.log" {
		t.Errorf("second stage: %v", paths)
	}
	if !strings.Contains(out, "Examined: group shop") || !strings.Contains(out, "blog.log") {
		t.Errorf("output:\n%s", out)
	}

	fake, _, errs, code = runWith("--group", "blog")
	if code != exitOK || len(fake.queries) != 1 || len(fake.queries[0].Logs) != 2 || filepath.Base(fake.queries[0].Logs[1].Path) != "blog.log" {
		t.Errorf("--group: exit %d %+v %s", code, fake.queries, errs)
	}
	if _, _, errs, code = runWith("--group", "nope"); code != exitUsage || !strings.Contains(errs, `group "nope"`) {
		t.Errorf("unknown group: %d %s", code, errs)
	}
}
