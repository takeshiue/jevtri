package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/jev"
	"github.com/takeshiue/jevtri/internal/timefmt"
)

type fakeDetector struct {
	answer   string
	requests []jev.FormatRequest
	err      error
}

func (f *fakeDetector) AskFormat(_ context.Context, request jev.FormatRequest) (jev.FormatResult, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return jev.FormatResult{}, f.err
	}
	return jev.FormatResult{Model: "jev-test", Options: []jev.Option{{Name: f.answer, Probability: 0.9}, {Name: jev.NoFormat, Probability: 0.1}}}, nil
}

var now = time.Date(2026, 9, 28, 4, 0, 0, 0, time.FixedZone("JST", 9*3600))

func jevOptions(t *testing.T, detector *fakeDetector, key error) Options {
	dir := t.TempDir()
	return Options{
		ConfigPath:  filepath.Join(dir, "jevtri.conf"),
		Now:         now,
		SentLog:     filepath.Join(dir, "sent.log"),
		LoadAPIKey:  func() (string, error) { return "k", key },
		NewDetector: func(string) Detector { return detector },
	}
}

const appLine = "=2026-09-28 03:02:30 ERROR login failed password=Example-Only-123 for alice@example.com\n2026-09-28 03:02:31 INFO retry\n"

// IN-03: a log with an unknown format is sent masked, and Jev's answer is
// written only after it reads the lines locally.
func TestDetectOnFirstRun(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	var out bytes.Buffer
	if err := Run(strings.NewReader("/opt/app/app.log\n\ny\n"), &out, opts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(detector.requests) != 1 {
		t.Fatalf("%d requests", len(detector.requests))
	}
	sent := detector.requests[0].State.Lines
	if strings.Contains(sent, "Example-Only-123") || strings.Contains(sent, "alice@") || !strings.Contains(sent, "2026-09-28 03:02:30 ERROR") {
		t.Errorf("unexpected lines sent: %s", sent)
	}
	if len(detector.requests[0].Questions["time_format"].Criteria) != len(timefmt.Names())+1 {
		t.Error("every catalog format and none must be offered")
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil || cfg.Logs[0].TimeFormat != "iso-space" {
		t.Fatalf("config %+v, %v\n%s", cfg, err, out.String())
	}
	record, _ := os.ReadFile(opts.SentLog)
	if !strings.Contains(string(record), `"purpose":"time_format"`) {
		t.Errorf("the send was not recorded: %s", record)
	}
}

func TestDetectDeclined(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	var out bytes.Buffer
	if err := Run(strings.NewReader("/opt/app/app.log\n\nn\n"), &out, opts); err != nil {
		t.Fatal(err)
	}
	if len(detector.requests) != 0 || !strings.Contains(out.String(), "Nothing was sent") {
		t.Errorf("sent although declined:\n%s", out.String())
	}
	if _, err := os.Stat(opts.SentLog); err == nil {
		t.Error("a send log was written although nothing was sent")
	}
}

// Jev can be wrong: an answer that does not read the lines is not written,
// and neither is "none".
func TestDetectRejectsUnverifiedAnswers(t *testing.T) {
	for _, answer := range []string{"syslog", "epoch", jev.NoFormat} {
		root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": "=order 1234567890 created\n"})
		opts := jevOptions(t, &fakeDetector{answer: answer}, nil)
		opts.Root = root
		var out bytes.Buffer
		if err := Run(strings.NewReader("/opt/app/app.log\n\n\n"), &out, opts); err != nil {
			t.Fatal(err)
		}
		cfg, loadError := config.Load(opts.ConfigPath)
		if loadError != nil {
			t.Fatalf("cannot load generated config: %v\n%s", loadError, out.String())
		}
		if cfg.Logs[0].TimeFormat != "" || !strings.Contains(out.String(), "% directives") {
			t.Errorf("%s: got %q\n%s", answer, cfg.Logs[0].TimeFormat, out.String())
		}
	}
}

func TestDetectWithoutKey(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, errors.New("cannot read the API key file /etc/jevtri/api-key"))
	opts.Root = root
	var out bytes.Buffer
	if err := Run(strings.NewReader("/opt/app/app.log\n\n\n"), &out, opts); err != nil {
		t.Fatal(err)
	}
	if len(detector.requests) != 0 || !strings.Contains(out.String(), "run 'jevtri init' again") {
		t.Errorf("unexpected:\n%s", out.String())
	}
}

func TestDetectFailureIsRecorded(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	opts := jevOptions(t, &fakeDetector{err: jev.ErrUnavailable}, nil)
	opts.Root = root
	var out bytes.Buffer
	if err := Run(strings.NewReader("/opt/app/app.log\n\n\n"), &out, opts); err != nil {
		t.Fatal(err)
	}
	record, _ := os.ReadFile(opts.SentLog)
	if !strings.Contains(string(record), `"error":"jev could not be reached"`) {
		t.Errorf("failure not recorded: %s", record)
	}
}

// IN-03 and IN-05: 'jevtri init' on an existing configuration adds time_format to the
// logs that lack it and keeps every other line.
func TestCompleteExisting(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	original := "# my comment\n[general]\nminutes = 10\n\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n\n[log app]\npath = /opt/app/app.log\n" + notSetComment + "\nmask = order-\\d+\n"
	os.WriteFile(opts.ConfigPath, []byte(original), 0o640)
	var out bytes.Buffer
	if err := Run(strings.NewReader("\n"), &out, opts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := strings.Replace(original, "path = /opt/app/app.log\n"+notSetComment+"\n", "path = /opt/app/app.log\ntime_format = iso-space\n", 1)
	data, _ := os.ReadFile(opts.ConfigPath)
	if string(data) != want {
		t.Errorf("got:\n%s\nwant:\n%s", data, want)
	}
	if info, _ := os.Stat(opts.ConfigPath); info.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", info.Mode().Perm())
	}
	if len(detector.requests) != 1 || detector.requests[0].State.Path != "/opt/app/app.log" {
		t.Errorf("unexpected requests %+v", detector.requests)
	}
}

func TestEveryFormatHasAnExample(t *testing.T) {
	for _, name := range timefmt.Names() {
		if formatExamples[name] == "" {
			t.Errorf("%s has no example", name)
		}
	}
}

// SEC-006: the lines that would be sent are shown before the consent, and no
// answer at all (end of input) is not consent.
func TestConsentShowsLinesAndRefusesOnEOF(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	var out bytes.Buffer
	// The log path, an empty line to finish, and then no answer to [Y/n].
	if err := Run(strings.NewReader("/opt/app/app.log\n\n"), &out, opts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(detector.requests) != 0 {
		t.Errorf("%d requests were sent without consent", len(detector.requests))
	}
	if !strings.Contains(out.String(), "No answer; nothing was sent") {
		t.Errorf("no refusal message:\n%s", out.String())
	}
	// The masked lines themselves must be shown before the question.
	shown, question := out.String(), "Send them to Jev? [Y/n]"
	before := shown[:strings.Index(shown, question)]
	if !strings.Contains(before, "| 2026-09-28 03:02:30 ERROR login failed password=") {
		t.Errorf("the lines are not shown before the question:\n%s", before)
	}
	if strings.Contains(before, "Example-Only-123") {
		t.Errorf("the shown lines are not masked:\n%s", before)
	}
}

// SEC-006: a log that is a symbolic link is not offered and not read, so a
// service that can write its log directory cannot have root send /etc/shadow.
func TestSymlinkedLogIsNotUsed(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{
		"/var/log/postgresql/postgresql-main.log": "=2026-09-28 03:02:30 LOG  checkpoint complete\n",
		"/etc/shadow": "=root:$6$invented$HASH:19000:0:99999:7:::\n",
	})
	link := filepath.Join(root, "var", "log", "postgresql", "postgresql-evil.log")
	if err := os.Symlink(filepath.Join(root, "etc", "shadow"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	var out bytes.Buffer
	// Empty selection takes all candidates; "y" would agree to send.
	if err := Run(strings.NewReader("\n\ny\n"), &out, opts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "postgresql-evil") {
		t.Errorf("the symlink was offered:\n%s", out.String())
	}
	for _, request := range detector.requests {
		if strings.Contains(request.State.Lines, "HASH") || strings.Contains(request.State.Path, "evil") {
			t.Errorf("the contents of the link target were sent: %+v", request.State)
		}
	}
	if data, err := os.ReadFile(opts.ConfigPath); err == nil && strings.Contains(string(data), "postgresql-evil") {
		t.Errorf("the symlink was written to the configuration:\n%s", data)
	}
}

// A sampled line goes straight to the terminal, so escape sequences in a log
// must not reach it (SEC-005).
func TestHeadLinesEscapesControlCharacters(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "evil.log"), []byte("Sep 30 12:00:00 host app: \x1b[2Jcleared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := headLines(dir, "evil.log")
	if len(lines) == 0 {
		t.Fatal("no lines sampled")
	}
	if strings.Contains(lines[0], "\x1b") {
		t.Errorf("escape sequence reached the terminal: %q", lines[0])
	}
	if !strings.Contains(lines[0], `\x1b`) {
		t.Errorf("the escape was not made visible: %q", lines[0])
	}
}
