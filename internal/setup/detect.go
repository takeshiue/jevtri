package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
	"github.com/takeshiue/jevtri/internal/logread"
	"github.com/takeshiue/jevtri/internal/mask"
	"github.com/takeshiue/jevtri/internal/printsafe"
	"github.com/takeshiue/jevtri/internal/safeopen"
	"github.com/takeshiue/jevtri/internal/sentlog"
	"github.com/takeshiue/jevtri/internal/timefmt"
)

// Detector asks Jev for a log's time format; jev.Client implements it.
type Detector interface {
	AskFormat(ctx context.Context, request jev.FormatRequest) (jev.FormatResult, error)
}

// formatExamples shows Jev what each catalog format looks like (spec 12.0).
var formatExamples = map[string]string{
	"syslog":        "Sep 28 07:51:24",
	"rfc3339":       "2026-09-28T07:29:16.929554+09:00 or 2026-09-27T18:02:14Z",
	"iso-space":     "2026-09-28 03:03:07.591 JST or 2026-09-28  3:02:36",
	"iso-comma":     "2026-09-28 15:47:01,123",
	"slash-ymd":     "2026/09/28 03:02:04",
	"apache-access": "[28/Sep/2026:09:44:10 +0900]",
	"apache-error":  "[Mon Sep 28 03:02:10.715837 2026]",
	"dmy-month":     "28-Sep-2026 09:44:02.397 or [28-Sep-2026 03:03:34] or 28 Sep 2026 03:03:22.943",
	"slash-mdy":     "09/28/2026 15:47:01 (month first)",
	"slash-dmy":     "28/09/2026 15:47:01 (day first)",
	"epoch":         "1790532211 or msg=audit(1790532211.123:456) (seconds since 1970)",
}

func formatChoices() []jev.FormatChoice {
	var choices []jev.FormatChoice
	for _, name := range timefmt.Names() {
		choices = append(choices, jev.FormatChoice{Name: name, Example: formatExamples[name]})
	}
	return choices
}

const (
	sampleLines   = 10
	maxLineLength = 300
)

// headLines returns the first non-blank lines of a log, masked, for the
// format question. A pattern is sampled from its newest file and an empty
// log from its newest rotated file.
func headLines(root, path string) []string {
	file := filepath.Join(root, path)
	if logread.IsPattern(path) {
		matches := readableMatches(root, path)
		if len(matches) == 0 {
			return nil
		}
		file = filepath.Join(root, matches[0])
	}
	lines := firstLines(file)
	if len(lines) == 0 {
		lines = firstLines(newestRotated(file))
	}
	masker, err := mask.New(nil)
	if err != nil {
		return nil
	}
	for i, line := range lines {
		if len(line) > maxLineLength {
			line = line[:maxLineLength]
		}
		// Escape control characters: a sampled line goes straight to the terminal.
		lines[i] = printsafe.Line(masker.Apply(line).Text)
	}
	return lines
}

func firstLines(path string) []string {
	handle, err := safeopen.Open(path)
	if err != nil {
		return nil
	}
	defer handle.Close()
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() && len(lines) < sampleLines {
		if strings.TrimSpace(scanner.Text()) != "" {
			lines = append(lines, scanner.Text())
		}
	}
	return lines
}

// readsLines checks Jev's choice locally (spec O-04: Jev can be wrong). The
// format must find a timestamp in at least one line, and every timestamp
// found must be plausible, which rejects "epoch" matching an arbitrary
// ten-digit number.
func readsLines(name string, lines []string, now time.Time) bool {
	format, err := timefmt.Resolve(name)
	if err != nil {
		return false
	}
	found := 0
	for _, line := range lines {
		m, ok := format.Find(line, time.Local)
		if !ok {
			continue
		}
		t := timefmt.ResolveYear(m, now)
		if t.Before(now.AddDate(-5, 0, 0)) || t.After(now.Add(24*time.Hour)) {
			return false
		}
		found++
	}
	return found > 0
}

// detectWithJev fills TimeFormat of the given logs where Jev's answer can be
// verified. Nothing is sent before the user agrees to it.
func detectWithJev(reader *bufio.Reader, out io.Writer, logs []*Candidate, opts Options) {
	if len(logs) == 0 {
		return
	}
	// The spec requires showing the logs and the lines themselves before the
	// consent, so read them now and ask about what will really be sent.
	heads := make(map[string][]string, len(logs))
	var ready []*Candidate
	for _, c := range logs {
		lines := headLines(opts.Root, c.Path)
		if len(lines) == 0 {
			fmt.Fprintf(out, "  %s: the log is empty; run 'jevtri init' again once it has lines\n", c.Path)
			continue
		}
		heads[c.Path] = lines
		ready = append(ready, c)
	}
	if len(ready) == 0 {
		return
	}
	fmt.Fprintf(out, "\nThe time format of these logs is not known. jevtri can ask Jev (%s)\n", jev.Endpoint)
	fmt.Fprintf(out, "to identify it. Exactly these lines would be sent (%d per log at most, already masked):\n", sampleLines)
	for _, c := range ready {
		fmt.Fprintf(out, "\n  %s:\n", c.Path)
		for _, line := range heads[c.Path] {
			fmt.Fprintf(out, "    | %s\n", line)
		}
	}
	fmt.Fprintln(out, "\nThe send is recorded in the send log. Nothing else is sent.")
	fmt.Fprint(out, "Send them to Jev? [Y/n]: ")
	answer, rerr := reader.ReadString('\n')
	// No answer at all (end of input, closed terminal) is not consent.
	if rerr != nil && strings.TrimSpace(answer) == "" {
		fmt.Fprintln(out, "\nNo answer; nothing was sent.")
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "n") {
		fmt.Fprintln(out, "Nothing was sent.")
		return
	}
	if opts.LoadAPIKey == nil || opts.NewDetector == nil {
		fmt.Fprintln(out, "Jev is not available here; nothing was sent.")
		return
	}
	key, err := opts.LoadAPIKey()
	if err != nil {
		fmt.Fprintf(out, "Cannot ask Jev: %v\nPut the key there and run 'jevtri init' again. Nothing was sent.\n", err)
		return
	}
	detector := opts.NewDetector(key)
	for _, c := range ready {
		lines := heads[c.Path]
		request := jev.BuildFormatRequest(c.Path, strings.Join(lines, "\n"), formatChoices())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := detector.AskFormat(ctx, request)
		cancel()
		var recorded *jev.FormatResult
		if err == nil {
			recorded = &result
		}
		if opts.SentLog != "" {
			if werr := sentlog.Append(opts.SentLog, sentlog.FormatEntry(opts.Now, request, recorded, err)); werr != nil {
				fmt.Fprintf(out, "  warning: %v\n", werr)
			}
		}
		if err != nil {
			fmt.Fprintf(out, "  %s: %v\n", c.Path, err)
			continue
		}
		best := result.Best()
		switch {
		case best.Name == jev.NoFormat:
			fmt.Fprintf(out, "  %s: no catalog format fits; write time_format with %% directives (see jevtri(1))\n", c.Path)
		case !readsLines(best.Name, lines, opts.Now):
			fmt.Fprintf(out, "  %s: Jev suggested %s, but it does not read the lines; write time_format with %% directives (see jevtri(1))\n", c.Path, best.Name)
		default:
			c.TimeFormat = best.Name
			fmt.Fprintf(out, "  %s: %s (checked on the first lines)\n", c.Path, best.Name)
		}
	}
}

// updateTimeFormats writes time_format into the named [log] sections of an
// existing configuration, keeping every other line as it is.
func updateTimeFormats(path string, formats map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	var out []string
	section := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = ""
			if fields := strings.Fields(trimmed[1 : len(trimmed)-1]); len(fields) == 2 && fields[0] == "log" {
				section = fields[1]
			}
		}
		format, target := formats[section]
		if target && trimmed == notSetComment {
			continue
		}
		out = append(out, line)
		if key, _, ok := strings.Cut(trimmed, "="); target && ok && strings.TrimSpace(key) == "path" {
			out = append(out, "time_format = "+format)
		}
	}
	// A fresh file each time (O_EXCL), flushed before the rename, so a leftover
	// .tmp cannot lend its permissions and a crash cannot leave a half file.
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %v", path, err)
	}
	name := temporary.Name()
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		os.Remove(name)
		return fmt.Errorf("cannot set the mode of %s: %v", name, err)
	}
	if _, err := temporary.WriteString(strings.Join(out, "\n") + "\n"); err != nil {
		temporary.Close()
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", name, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		os.Remove(name)
		return fmt.Errorf("cannot flush %s: %v", name, err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %v", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("cannot replace %s: %v", path, err)
	}
	return nil
}
