package journal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeJournalctl installs a shell script as root/usr/bin/journalctl that
// records its arguments and prints body.
func fakeJournalctl(t *testing.T, body string) (root, argsFile string) {
	t.Helper()
	root = t.TempDir()
	argsFile = filepath.Join(root, "args")
	path := filepath.Join(root, "usr", "bin", "journalctl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, argsFile
}

// LR-07: journalctl gets the window as epoch seconds and the unit, without a
// shell, and its output is returned.
func TestOutputPassesWindowAndUnit(t *testing.T) {
	root, argsFile := fakeJournalctl(t, "echo '2026-10-01T10:00:05.123456+09:00 host dockerd[1]: started'")
	journalctl, err := Find(root)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 9, 55, 0, 0, time.UTC)
	end := start.Add(10 * time.Minute)
	out, truncated, err := Output(journalctl, "docker.service", start, end, 1<<20)
	if err != nil || truncated || !strings.Contains(string(out), "dockerd[1]: started") {
		t.Fatalf("%q %v %v", out, truncated, err)
	}
	args, _ := os.ReadFile(argsFile)
	want := "--since\n@" + itoa(start.Unix()) + "\n--until\n@" + itoa(end.Unix()+1) + "\n-o\nshort-iso-precise\n--no-pager\n-q\n-u\ndocker.service\n"
	if string(args) != want {
		t.Errorf("arguments:\n%s\nwant:\n%s", args, want)
	}
	// "*" reads every unit: no -u.
	Output(journalctl, "*", start, end, 1<<20)
	args, _ = os.ReadFile(argsFile)
	if strings.Contains(string(args), "\n-u\n") {
		t.Errorf("-u given for *: %s", args)
	}
}

func itoa(n int64) string { return formatInt(n) }

func formatInt(n int64) string {
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestOutputLimitAndFailure(t *testing.T) {
	root, _ := fakeJournalctl(t, "yes 2026-10-01T10:00:05+09:00 line | head -n 100000")
	journalctl, _ := Find(root)
	now := time.Now()
	out, truncated, err := Output(journalctl, "*", now, now, 1000)
	if err != nil || !truncated || len(out) != 1000 {
		t.Errorf("limit: %d %v %v", len(out), truncated, err)
	}

	root, _ = fakeJournalctl(t, "echo 'Failed to add match: Invalid argument' >&2; exit 1")
	journalctl, _ = Find(root)
	if _, _, err := Output(journalctl, "bad", now, now, 1000); err == nil || !strings.Contains(err.Error(), "Invalid argument") {
		t.Errorf("failure: %v", err)
	}

	saved := Timeout
	Timeout = 200 * time.Millisecond
	defer func() { Timeout = saved }()
	root, _ = fakeJournalctl(t, "exec sleep 5")
	journalctl, _ = Find(root)
	if _, _, err := Output(journalctl, "*", now, now, 1000); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("timeout: %v", err)
	}
}

func TestFindWithoutJournalctl(t *testing.T) {
	if _, err := Find(t.TempDir()); err != ErrNoJournalctl {
		t.Errorf("%v", err)
	}
}
