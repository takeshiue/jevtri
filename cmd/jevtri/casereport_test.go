package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f *fixture) report(input string) int {
	jst := time.FixedZone("JST", 9*3600)
	root := filepath.Join(f.dir, "root")
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n"), 0o644)
	env := environment{
		stdin: strings.NewReader(input), stdout: &f.stdout, stderr: &f.stderr,
		now:        func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, jst) },
		isTerminal: func() bool { return true },
		root:       root,
		hostname:   func() (string, error) { return "web01.example.net", nil },
	}
	return run([]string{"-c", f.conf, "report"}, env)
}

// RP-01: a run that Jev answered becomes a report file with the chosen cause; host
// names and addresses are replaced, and nothing is sent (newAsker is nil).
func TestReportFromSendLog(t *testing.T) {
	f := newFixture(t, "")
	appLog := filepath.Join(f.dir, "app.log")
	os.WriteFile(appLog, []byte("2026-09-28 03:02:30 ERROR web01 cannot reach 198.51.100.7 from web01.example.net; 198.51.100.7 again\n"), 0o644)
	if code := f.run("-t", "03:02", "-m", "5", "-i", "Site down on web01"); code != exitOK {
		t.Fatalf("ranking exit %d: %s", code, f.stderr.String())
	}
	f.stdout.Reset()
	// run 1, cause 3 (app ranks last in the fake), a note
	if code := f.report("1\n3\nweb01 ran out of connections\n"); code != exitOK {
		t.Fatalf("report exit %d: %s", code, f.stderr.String())
	}
	path := filepath.Join(f.dir, "state", "case-20260929-100000.md")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report not written with 0600: %v", err)
	}
	data, _ := os.ReadFile(path)
	body := string(data)
	for _, want := range []string{"### Real cause\n\n- <code>" + appLog + "</code>", "[host-1] ran out of connections", "Site down on [host-1]",
		"ERROR [host-1] cannot reach [ip-3] from [host-2]; [ip-3] again", "OS: <code>Ubuntu 24.04.2 LTS</code>", "jevtri: <code>" + version + "</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("report lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "web01") || strings.Contains(body, "198.51.100.7") || strings.Contains(body, "Example-Only-123") {
		t.Errorf("host name, address or secret left in the report:\n%s", body)
	}
	if !strings.Contains(f.stdout.String(), "https://github.com/takeshiue/jevtri/issues/new?") || !strings.Contains(f.stdout.String(), "Nothing was sent") {
		t.Errorf("unexpected output:\n%s", f.stdout.String())
	}
	// A second report in the same second never overwrites the first.
	if code := f.report("1\n0\n\n"); code != exitFailure {
		t.Errorf("existing report was replaced, exit %d", code)
	}
}

// RP-01: dry runs are not reports: there is no answer to judge.
func TestReportNeedsAnsweredRun(t *testing.T) {
	f := newFixture(t, "")
	if code := f.run("-t", "03:02", "-m", "5", "--dry-run"); code != exitOK {
		t.Fatalf("dry run exit %d", code)
	}
	if code := f.report("1\n"); code != exitFailure || !strings.Contains(f.stderr.String(), "nothing to report") {
		t.Errorf("exit %d, stderr %s", code, f.stderr.String())
	}
}

// RP-01: without a terminal, report refuses.
func TestReportNeedsTerminal(t *testing.T) {
	f := newFixture(t, "")
	if code := f.run("report"); code != exitFailure || !strings.Contains(f.stderr.String(), "needs a terminal") {
		t.Errorf("exit %d, stderr %s", code, f.stderr.String())
	}
}
