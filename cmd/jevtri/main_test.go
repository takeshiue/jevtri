package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// Logs without a timezone setting are read in the server's zone (time.Local),
// and the fixtures are written for a server in JST; CI runs in UTC.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("JST", 9*3600)
	os.Exit(m.Run())
}

type fakeAsker struct {
	query jev.Query
	err   error
}

func (f *fakeAsker) Ask(_ context.Context, q jev.Query) (jev.Result, error) {
	f.query = q
	if f.err != nil {
		return jev.Result{}, f.err
	}
	var scores []jev.Score
	for _, log := range q.Logs {
		raw := 1.0
		if strings.Contains(log.Path, "nginx") {
			raw = 2.9
		}
		scores = append(scores, jev.Score{Path: log.Path, Raw: raw, Priority: raw / 3, Confidence: 0.9})
	}
	return jev.Result{Model: "jev-test", Scores: scores, Latency: time.Millisecond}, nil
}

func (f *fakeAsker) AskFormat(context.Context, jev.FormatRequest) (jev.FormatResult, error) {
	return jev.FormatResult{Options: []jev.Option{{Name: jev.NoFormat, Probability: 1}}}, nil
}

type fixture struct {
	dir, conf, key string
	asker          *fakeAsker
	stdout, stderr bytes.Buffer
}

func newFixture(t *testing.T, extraLogs string) *fixture {
	t.Helper()
	dir := t.TempDir()
	samples, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "logs"))
	appLog := filepath.Join(dir, "app.log")
	os.WriteFile(appLog, []byte("2026-09-28 03:02:30 ERROR login failed password=Example-Only-123 for alice@example.com\n"), 0o644)
	conf := filepath.Join(dir, "jevtri.conf")
	content := fmt.Sprintf(`[general]
sent_log = %s

[log nginx]
path = %s
time_format = slash-ymd

[log httpd]
path = %s
time_format = apache-error

[log app]
path = %s
time_format = iso-space
%s`, filepath.Join(dir, "state", "sent.log"), filepath.Join(samples, "nginx", "error.log"), filepath.Join(samples, "httpd", "error_log"), appLog, extraLogs)
	os.WriteFile(conf, []byte(content), 0o644)
	key := filepath.Join(dir, "api-key")
	os.WriteFile(key, []byte("test-key\n"), 0o600)
	return &fixture{dir: dir, conf: conf, key: key, asker: &fakeAsker{}}
}

func (f *fixture) run(args ...string) int {
	jst := time.FixedZone("JST", 9*3600)
	env := environment{
		stdout: &f.stdout, stderr: &f.stderr,
		now:        func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, jst) },
		newAsker:   func(string) asker { return f.asker },
		keyPath:    f.key,
		isTerminal: func() bool { return false },
		getenv:     func(string) string { return "" },
	}
	return run(append([]string{"-c", f.conf}, args...), env)
}

func TestRankingEndToEnd(t *testing.T) {
	f := newFixture(t, "")
	code := f.run("-t", "03:02", "-m", "5", "-i", "The website cannot be reached")
	if code != exitOK {
		t.Fatalf("exit %d, stderr: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "Start with: ") || !strings.Contains(f.stdout.String(), "nginx/error.log") {
		t.Errorf("unexpected output:\n%s", f.stdout.String())
	}
	if len(f.asker.query.Logs) != 3 || f.asker.query.Symptom != "The website cannot be reached" {
		t.Fatalf("unexpected query %+v", f.asker.query)
	}
	sent := ""
	for _, log := range f.asker.query.Logs {
		sent += log.Text
	}
	if strings.Contains(sent, "Example-Only-123") || strings.Contains(sent, "alice@") {
		t.Error("secrets were sent unmasked")
	}
	if !strings.Contains(sent, "2026-09-28T03:02:05.000+09:00 [emerg]") {
		t.Errorf("timestamps not normalised in:\n%s", sent)
	}
	info, err := os.Stat(filepath.Join(f.dir, "state", "sent.log"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("send log not written with 0600: %v", err)
	}
}

// EX-01: an unreadable log is reported, not ranked, and the exit code is 2.
func TestPartial(t *testing.T) {
	f := newFixture(t, "\n[log missing]\npath = /nonexistent/x.log\ntime_format = syslog\n")
	if code := f.run("-t", "03:02", "--json"); code != exitPartial {
		t.Fatalf("exit %d, want %d; stderr %s", code, exitPartial, f.stderr.String())
	}
	var out struct {
		Results      []struct{ Log string }
		NotEvaluated []struct{ Log, Reason string } `json:"not_evaluated"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 3 || len(out.NotEvaluated) != 1 || out.NotEvaluated[0].Log != "/nonexistent/x.log" {
		t.Errorf("unexpected %+v", out)
	}
}

// EX-04: a log without timestamps is not sent and is listed as not evaluated.
func TestLogWithoutTimestamps(t *testing.T) {
	f := newFixture(t, "")
	boot := filepath.Join(f.dir, "boot.log")
	os.WriteFile(boot, []byte("[FAILED] Failed to start nginx.service.\n"), 0o644)
	content, _ := os.ReadFile(f.conf)
	os.WriteFile(f.conf, append(content, fmt.Sprintf("\n[log boot]\npath = %s\ntime_format = syslog\n", boot)...), 0o644)
	if code := f.run("-t", "03:02", "--json"); code != exitPartial {
		t.Fatalf("exit %d, want %d; stderr %s", code, exitPartial, f.stderr.String())
	}
	for _, log := range f.asker.query.Logs {
		if log.Path == boot {
			t.Fatal("a log without timestamps was sent")
		}
	}
	var out struct {
		NotEvaluated []struct{ Log, Reason string } `json:"not_evaluated"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.NotEvaluated) != 1 || out.NotEvaluated[0].Log != boot || !strings.Contains(out.NotEvaluated[0].Reason, "no timestamps") {
		t.Errorf("unexpected %+v", out.NotEvaluated)
	}
}

// EX-05: a log with nothing in the window is not sent and has no rank.
func TestQuietLogIsNotSent(t *testing.T) {
	f := newFixture(t, "")
	quiet := filepath.Join(f.dir, "quiet.log")
	os.WriteFile(quiet, []byte("2026-09-27 10:00:00 INFO long before the incident\n"), 0o644)
	content, _ := os.ReadFile(f.conf)
	os.WriteFile(f.conf, append(content, fmt.Sprintf("\n[log quiet]\npath = %s\ntime_format = iso-space\n", quiet)...), 0o644)
	if code := f.run("-t", "03:02", "--json"); code != exitOK {
		t.Fatalf("exit %d; stderr %s", code, f.stderr.String())
	}
	for _, log := range f.asker.query.Logs {
		if log.Path == quiet {
			t.Fatal("a log with nothing in the window was sent")
		}
	}
	var out struct {
		Results   []struct{ Log string }
		NoEntries []string `json:"no_entries"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 3 || len(out.NoEntries) != 1 || out.NoEntries[0] != quiet {
		t.Errorf("unexpected %+v", out)
	}
}

// EX-05: when every log is quiet nothing is sent and the user is told why.
func TestAllLogsQuiet(t *testing.T) {
	f := newFixture(t, "")
	quiet := filepath.Join(f.dir, "quiet.log")
	os.WriteFile(quiet, []byte("2026-09-27 10:00:00 INFO long before the incident\n"), 0o644)
	os.WriteFile(f.conf, []byte(fmt.Sprintf("[log quiet]\npath = %s\ntime_format = iso-space\n", quiet)), 0o644)
	if code := f.run("-t", "03:02"); code != exitOK {
		t.Fatalf("exit %d; stderr %s", code, f.stderr.String())
	}
	if f.asker.query.Logs != nil {
		t.Error("Jev was asked although no log had entries in the window")
	}
	if !strings.Contains(f.stdout.String(), "nothing was sent to Jev") || !strings.Contains(f.stdout.String(), quiet) {
		t.Errorf("unexpected output:\n%s", f.stdout.String())
	}
}

// EX-02: nothing readable means nothing is sent.
func TestNothingReadable(t *testing.T) {
	f := newFixture(t, "")
	os.WriteFile(f.conf, []byte("[log a]\npath = /nonexistent/a\ntime_format = syslog\n"), 0o644)
	if code := f.run(); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if f.asker.query.Logs != nil {
		t.Error("Jev was asked although no log was readable")
	}
}

// EX-03: a key file readable by others stops the run before sending.
func TestLooseKeyFile(t *testing.T) {
	f := newFixture(t, "")
	os.Chmod(f.key, 0o644)
	if code := f.run("-t", "03:02"); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if f.asker.query.Logs != nil || !strings.Contains(f.stderr.String(), "chmod 600") {
		t.Errorf("unexpected: %s", f.stderr.String())
	}
}

func TestJevFailure(t *testing.T) {
	f := newFixture(t, "")
	f.asker.err = fmt.Errorf("%w: HTTP 503", jev.ErrUnavailable)
	if code := f.run("-t", "03:02"); code != exitJevFailure {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(f.stdout.String(), "Start with") {
		t.Error("a ranking was printed although Jev failed")
	}
}

// JV-01: --dry-run prints the request and sends nothing.
func TestDryRun(t *testing.T) {
	f := newFixture(t, "")
	os.Remove(f.key) // not needed for a dry run
	if code := f.run("-t", "03:02", "--dry-run"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	var request jev.Request
	if err := json.Unmarshal(f.stdout.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Questions) != 3 || f.asker.query.Logs != nil {
		t.Errorf("unexpected request or a send happened: %+v", request.Questions)
	}
	if !strings.Contains(f.stdout.String(), "[MASKED:secret]") {
		t.Error("dry run output is not masked")
	}
}

// IN-01: without a configuration, a run from a terminal starts init and
// the written file is usable.
func TestInitFromTerminal(t *testing.T) {
	f := newFixture(t, "")
	os.Remove(f.conf)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nID_LIKE=debian\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "var", "log", "nginx"), 0o755)
	sample, _ := os.ReadFile(filepath.Join("..", "..", "testdata", "logs", "nginx", "error.log"))
	os.WriteFile(filepath.Join(root, "var", "log", "nginx", "error.log"), sample, 0o644)
	env := environment{
		stdin: strings.NewReader("\n\n"), stdout: &f.stdout, stderr: &f.stderr,
		now:        time.Now,
		newAsker:   func(string) asker { return f.asker },
		keyPath:    f.key,
		isTerminal: func() bool { return true },
		root:       root,
	}
	if code := run([]string{"-c", f.conf}, env); code != exitFailure {
		// -c is not the default path, so init is not started automatically.
		t.Fatalf("exit %d", code)
	}
	if code := run([]string{"-c", f.conf, "init"}, env); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	data, _ := os.ReadFile(f.conf)
	if !strings.Contains(string(data), "[log nginx-error]\npath = /var/log/nginx/error.log\ntime_format = slash-ymd\ngroup = system\n") {
		t.Errorf("unexpected configuration:\n%s", data)
	}
}

// IN-02: without a terminal, init refuses instead of writing a configuration.
func TestInitWithoutTerminal(t *testing.T) {
	f := newFixture(t, "")
	os.Remove(f.conf)
	if code := f.run("init"); code != exitFailure || !strings.Contains(f.stderr.String(), "needs a terminal") {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
}

// IN-02: a missing configuration without a terminal is an error, not a prompt.
func TestMissingConfigWithoutTerminal(t *testing.T) {
	f := newFixture(t, "")
	os.Remove(f.conf)
	if code := f.run(); code != exitFailure || !strings.Contains(f.stderr.String(), "jevtri init") {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
}

// The priority must not be read as a cause probability (spec O-06), and what
// leaves the host must be stated (O-08).
func TestHelpStatesMeaningAndSending(t *testing.T) {
	f := newFixture(t, "")
	if code := f.run("--help"); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"not the probability", "api.typesafe.ai", "--dry-run", "not masked", "sent.log"} {
		if !strings.Contains(f.stdout.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, f.stdout.String())
		}
	}
}

// SEC-011: logs are not sent when the run cannot be recorded (spec 12.2).
func TestNotSentWhenTheSendLogCannotBeWritten(t *testing.T) {
	f := newFixture(t, "")
	// The send log's directory is a file, so no record can be appended.
	blocked := filepath.Join(f.dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(f.conf)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(conf), filepath.Join(f.dir, "state", "sent.log"), filepath.Join(blocked, "sent.log"), 1)
	if err := os.WriteFile(f.conf, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.run("-t", "03:02"); code != exitFailure {
		t.Fatalf("exit %d, want %d\n%s", code, exitFailure, f.stderr.String())
	}
	if len(f.asker.query.Logs) != 0 {
		t.Errorf("%d logs were sent without a record", len(f.asker.query.Logs))
	}
	if !strings.Contains(f.stderr.String(), "nothing was sent") {
		t.Errorf("stderr does not say nothing was sent:\n%s", f.stderr.String())
	}
}

// SEC-007: a symbolic link at sent_log would make root write the file it
// points at (for example /etc/passwd).
func TestSendLogSymlinkIsRefused(t *testing.T) {
	f := newFixture(t, "")
	target := filepath.Join(f.dir, "victim")
	if err := os.WriteFile(target, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.dir, "state", "sent.log")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if code := f.run("-t", "03:02"); code != exitFailure {
		t.Errorf("exit %d, want %d\n%s", code, exitFailure, f.stderr.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "root:x:0:0:root:/root:/bin/bash\n" {
		t.Errorf("the link target was written:\n%s", data)
	}
	if info, err := os.Stat(target); err == nil && info.Mode().Perm() != 0o644 {
		t.Errorf("the link target's mode became %04o", info.Mode().Perm())
	}
}

// RW-14 (R-14): what reading left out is told on stderr, in -v and in
// --json, apart from what the send budget dropped.
func TestReadTruncationIsReported(t *testing.T) {
	f := newFixture(t, "")
	trace := filepath.Join(f.dir, "trace.log")
	body := "2026-09-28 03:02:00 ERROR start\n" + strings.Repeat("y", 5<<20) + "\n2026-09-28 03:02:30 ERROR LOST\n"
	os.WriteFile(trace, []byte(body), 0o644)
	content, _ := os.ReadFile(f.conf)
	os.WriteFile(f.conf, append(content, fmt.Sprintf("\n[log trace]\npath = %s\ntime_format = iso-space\n", trace)...), 0o644)

	if code := f.run("-t", "03:02", "-v"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), trace+": part of the window was left out while reading") {
		t.Errorf("no warning on stderr: %q", f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "read truncated:") {
		t.Errorf("-v does not show the read truncation:\n%s", f.stdout.String())
	}

	f.stdout.Reset()
	if code := f.run("-t", "03:02", "--json"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	var out struct {
		ReadTruncated []string `json:"read_truncated"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ReadTruncated) != 1 || out.ReadTruncated[0] != trace {
		t.Errorf("read_truncated = %v, want [%s]", out.ReadTruncated, trace)
	}
}

// RX-22 (R-22): the same when reading stopped before any entry was kept.
func TestReadTruncationWithNoEntriesIsReported(t *testing.T) {
	f := newFixture(t, "")
	trace := filepath.Join(f.dir, "trace.log")
	os.WriteFile(trace, []byte(strings.Repeat("x", 5<<20)+"\n2026-09-28 03:02:00 ERROR LOST\n"), 0o644)
	content, _ := os.ReadFile(f.conf)
	os.WriteFile(f.conf, append(content, fmt.Sprintf("\n[log trace]\npath = %s\ntime_format = iso-space\n", trace)...), 0o644)

	if code := f.run("-t", "03:02", "-v"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.stderr.String(), trace+": part of the window was left out while reading") {
		t.Errorf("no warning on stderr: %q", f.stderr.String())
	}
	if !strings.Contains(f.stdout.String(), "read truncated:") {
		t.Errorf("-v does not show the read truncation:\n%s", f.stdout.String())
	}

	f.stdout.Reset()
	if code := f.run("-t", "03:02", "--json"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	var out struct {
		ReadTruncated []string `json:"read_truncated"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ReadTruncated) != 1 || out.ReadTruncated[0] != trace {
		t.Errorf("read_truncated = %v, want [%s]", out.ReadTruncated, trace)
	}
}

// RX-31 (R-31): the only log kept nothing because reading stopped early.
func TestSummaryWhenOnlyLogIsTruncated(t *testing.T) {
	f := newFixture(t, "")
	trace := filepath.Join(f.dir, "trace.log")
	os.WriteFile(trace, []byte(strings.Repeat("x", 5<<20)+"\n2026-09-28 03:02:00 ERROR LOST\n"), 0o644)
	os.WriteFile(f.conf, []byte(fmt.Sprintf("[general]\nsent_log = %s\n\n[log trace]\npath = %s\ntime_format = iso-space\n",
		filepath.Join(f.dir, "state", "sent.log"), trace)), 0o644)
	if code := f.run("-t", "03:02", "-v"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if strings.Contains(f.stdout.String(), "No configured log has entries in the window") || !strings.Contains(f.stdout.String(), "Reading stopped early") {
		t.Errorf("summary does not reflect the read truncation:\n%s", f.stdout.String())
	}
	if f.asker.query.Logs != nil {
		t.Error("something was sent")
	}
}
