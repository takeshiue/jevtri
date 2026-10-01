package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/takeshiue/jevtri/internal/casereport"
	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/printsafe"
	"github.com/takeshiue/jevtri/internal/setup"
)

// maxListedRuns keeps the choice readable; older runs are rarely reported.
const maxListedRuns = 20

// runReport asks which log held the real cause of a past run and writes a
// report for the public issue form (spec 12.6). Nothing is sent.
func runReport(o options, env environment) int {
	if !env.isTerminal() {
		fmt.Fprintln(env.stderr, "jevtri: report asks questions and needs a terminal")
		return exitFailure
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	runs, err := casereport.LoadRuns(cfg.SentLog)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	if len(runs) == 0 {
		fmt.Fprintf(env.stderr, "jevtri: %s has no run that Jev answered; nothing to report\n", cfg.SentLog)
		return exitFailure
	}
	if len(runs) > maxListedRuns {
		runs = runs[:maxListedRuns]
	}
	in := bufio.NewReader(env.stdin)
	out := env.stdout

	fmt.Fprintln(out, "Runs that Jev answered (newest first):")
	for i, run := range runs {
		symptom := run.Symptom
		if symptom == "" {
			symptom = "(no -i)"
		}
		fmt.Fprintf(out, "  %2d. %s  %s\n      first: %s\n", i+1, run.Time.Format("2006-01-02 15:04"), printsafe.Line(shorten(symptom, 60)), printsafe.Line(run.Rankings[0].Path))
	}
	chosen, ok := askNumbers(in, out, fmt.Sprintf("Which run? [1-%d]: ", len(runs)), len(runs), false)
	if !ok {
		return exitFailure
	}
	run := runs[chosen[0]]

	fmt.Fprintln(out, "\nRanking of that run:")
	for i, ranking := range run.Rankings {
		fmt.Fprintf(out, "  %2d. %-40s %3d\n", i+1, printsafe.Line(ranking.Path), ranking.Priority)
	}
	causeIndexes, ok := askNumbers(in, out, "Which log showed the real cause? Numbers, or 0 if none of them: ", len(run.Rankings), true)
	if !ok {
		return exitFailure
	}
	var causes []string
	for _, i := range causeIndexes {
		causes = append(causes, run.Rankings[i].Path)
	}
	fmt.Fprint(out, "What was the cause, in one line (optional): ")
	note, _ := in.ReadString('\n')

	osRelease, _ := os.ReadFile(filepath.Join(env.root, "/etc/os-release"))
	c := casereport.Case{
		Run:           run,
		Causes:        causes,
		Note:          strings.TrimSpace(note),
		OS:            casereport.OSName(string(osRelease)),
		JevtriVersion: version,
	}
	passwd, _ := os.ReadFile(filepath.Join(env.root, "/etc/passwd"))
	pseudonymizer := casereport.NewPseudonymizer(hostnames(env), casereport.LocalUsers(string(passwd))...)
	body := casereport.Markdown(c, pseudonymizer)

	path := filepath.Join(filepath.Dir(cfg.SentLog), "case-"+env.now().Format("20060102-150405")+".md")
	if err := writeNew(path, body); err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	counts := pseudonymizer.Counts()
	fmt.Fprintf(out, "\nWrote %s\n", path)
	fmt.Fprintf(out, "Replaced %d host names, %d user names (UID 1000 and above) and %d IP addresses with labels such as [host-1], [user-1] and [ip-1].\n", counts["host"], counts["user"], counts["ip"])
	fmt.Fprintln(out, "Nothing was sent. The issue is public: read the file and remove anything else you do not want to publish,")
	fmt.Fprintln(out, "such as other host names, system account names, paths or customer data.")
	if len([]rune(body)) > casereport.MaxIssueBody {
		fmt.Fprintln(out, "The report is too long for the issue text; attach the file to the issue instead of pasting it.")
	} else {
		fmt.Fprintln(out, "Then paste it into the form, or attach the file:")
	}
	fmt.Fprintf(out, "  %s\n", casereport.FormURL(c, pseudonymizer))
	return exitOK
}

// askNumbers reads a selection until it is valid. allowNone accepts 0 for
// "none of them"; otherwise exactly one number is taken.
func askNumbers(in *bufio.Reader, out interface{ Write([]byte) (int, error) }, prompt string, count int, allowNone bool) ([]int, bool) {
	for {
		fmt.Fprint(out, prompt)
		answer, err := in.ReadString('\n')
		answer = strings.TrimSpace(answer)
		if err != nil && answer == "" {
			fmt.Fprintln(out, "\nNo answer; nothing was written.")
			return nil, false
		}
		if allowNone && answer == "0" {
			return nil, true
		}
		if answer != "" {
			if allowNone {
				indexes, perr := setup.ParseSelection(answer, count)
				if perr == nil {
					return indexes, true
				}
				fmt.Fprintf(out, "  %v\n", perr)
				continue
			}
			if n, perr := strconv.Atoi(answer); perr == nil && n >= 1 && n <= count {
				return []int{n - 1}, true
			}
		}
		fmt.Fprintf(out, "  enter a number between 1 and %d\n", count)
	}
}

func hostnames(env environment) []string {
	name, err := env.hostname()
	if err != nil || name == "" {
		return nil
	}
	short, _, _ := strings.Cut(name, ".")
	return []string{name, short}
}

// writeNew creates the report readable by root only, as it holds the same
// lines as the send log, and never follows or replaces an existing file.
func writeNew(path, body string) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("cannot write the report: %v", err)
	}
	if _, err := handle.WriteString(body); err != nil {
		handle.Close()
		return fmt.Errorf("cannot write the report %s: %v", path, err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("cannot write the report %s: %v", path, err)
	}
	return nil
}

func shorten(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}
