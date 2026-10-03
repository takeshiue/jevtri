// Package journal reads the systemd journal through journalctl for one
// window (spec 12.7.2). The journal's own files are never parsed.
package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Paths are the only places journalctl is run from; PATH is not searched so
// that a writable directory early in PATH cannot supply another program.
var Paths = []string{"/usr/bin/journalctl", "/bin/journalctl"}

// Timeout stops a journalctl that takes too long.
var Timeout = 30 * time.Second

// ErrNoJournalctl means none of Paths exists.
var ErrNoJournalctl = errors.New("journalctl was not found in /usr/bin or /bin")

// Find returns the journalctl to run, with root prepended (tests only).
func Find(root string) (string, error) {
	for _, path := range Paths {
		info, err := os.Stat(filepath.Join(root, path))
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return filepath.Join(root, path), nil
		}
	}
	return "", ErrNoJournalctl
}

// Args are the journalctl arguments for unit ("*" for every unit) and the
// window [start, end]. Times are given as seconds since the epoch, so the
// local zone of journalctl cannot shift the window.
func Args(unit string, start, end time.Time) []string {
	args := []string{
		"--since", "@" + strconv.FormatInt(start.Unix(), 10),
		// --until compares full timestamps, so entries within the last second of
		// the window come after @end; one more second keeps them. logread drops
		// whatever lies outside the window.
		"--until", "@" + strconv.FormatInt(end.Unix()+1, 10),
		"-o", "short-iso-precise", "--no-pager", "-q",
	}
	if unit != "*" {
		args = append(args, "-u", unit)
	}
	return args
}

// Output runs journalctl and returns at most limit bytes of its output.
// truncated reports that more was available.
func Output(journalctl, unit string, start, end time.Time, limit int64) (output []byte, truncated bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, journalctl, Args(unit, start, end)...)
	// Stop the whole process group, and do not wait for long for output that
	// a leftover child might still hold open.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 2 * time.Second
	// A fixed, minimal environment: no pager, no colors, C locale.
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0"}
	var stderr bytes.Buffer
	command.Stderr = &limitedWriter{buffer: &stderr, limit: 4096}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := command.Start(); err != nil {
		return nil, false, err
	}
	output, err = io.ReadAll(io.LimitReader(stdout, limit+1))
	if int64(len(output)) > limit {
		output, truncated = output[:limit], true
		cancel() // the rest is not needed
	}
	waitErr := command.Wait()
	switch {
	case err != nil:
		return nil, false, err
	case ctx.Err() == context.DeadlineExceeded:
		return nil, false, fmt.Errorf("journalctl did not finish within %s", Timeout)
	case waitErr != nil && !truncated:
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, false, fmt.Errorf("journalctl failed: %s", message)
	}
	return output, truncated, nil
}

// limitedWriter keeps the first limit bytes and drops the rest.
type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buffer.Len(); room > 0 {
		w.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
