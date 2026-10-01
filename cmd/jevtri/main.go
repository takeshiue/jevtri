// Command jevtri ranks server logs by how worth they are to examine first
// during an incident, using Jev.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/takeshiue/jevtri/internal/budget"
	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/jev"
	"github.com/takeshiue/jevtri/internal/logread"
	"github.com/takeshiue/jevtri/internal/mask"
	"github.com/takeshiue/jevtri/internal/report"
	"github.com/takeshiue/jevtri/internal/sentlog"
	"github.com/takeshiue/jevtri/internal/setup"
	"github.com/takeshiue/jevtri/internal/window"
)

// version is the single source of the application version (spec: one
// canonical place). Packaging may still override it with
// -ldflags "-X main.version=...", but must pass this same value.
var version = "0.1.0"

// Exit codes (spec 12.3).
const (
	exitOK          = 0
	exitFailure     = 1
	exitPartial     = 2
	exitJevFailure  = 3
	exitUsage       = 64
	defaultKeyPath  = config.DefaultAPIKeyPath
	defaultConfPath = config.DefaultPath
)

// asker is the part of jev.Client used here; tests replace it.
type asker interface {
	Ask(ctx context.Context, q jev.Query) (jev.Result, error)
	AskFormat(ctx context.Context, request jev.FormatRequest) (jev.FormatResult, error)
}

type environment struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	now            func() time.Time
	newAsker       func(apiKey string) asker
	keyPath        string
	isTerminal     func() bool
	root           string // prefix for the paths init looks at; tests only
	getenv         func(string) string
	hostname       func() (string, error)
}

func main() {
	env := environment{
		stdin:      os.Stdin,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		now:        time.Now,
		newAsker:   func(key string) asker { return jev.NewClient(key) },
		keyPath:    defaultKeyPath,
		isTerminal: stdinIsTerminal,
		getenv:     os.Getenv,
		hostname:   os.Hostname,
	}
	os.Exit(run(os.Args[1:], env))
}

type options struct {
	timeArgument string
	minutes      int
	issue        string
	configPath   string
	verbose      bool
	json         bool
	dryRun       bool
	help         bool
	version      bool
	lang         string
}

func parseOptions(args []string) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet("jevtri", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, name := range []string{"t", "time"} {
		fs.StringVar(&o.timeArgument, name, "", "")
	}
	for _, name := range []string{"m", "minutes"} {
		fs.IntVar(&o.minutes, name, 0, "")
	}
	for _, name := range []string{"i", "issue"} {
		fs.StringVar(&o.issue, name, "", "")
	}
	for _, name := range []string{"c", "config"} {
		fs.StringVar(&o.configPath, name, defaultConfPath, "")
	}
	for _, name := range []string{"v", "verbose"} {
		fs.BoolVar(&o.verbose, name, false, "")
	}
	for _, name := range []string{"j", "json"} {
		fs.BoolVar(&o.json, name, false, "")
	}
	for _, name := range []string{"h", "help"} {
		fs.BoolVar(&o.help, name, false, "")
	}
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.version, "version", false, "")
	fs.StringVar(&o.lang, "lang", "", "")
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	return o, fs.Args(), nil
}

func run(args []string, env environment) int {
	o, rest, err := parseOptions(args)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\nRun 'jevtri --help' for usage.\n", err)
		return exitUsage
	}
	switch {
	case o.help:
		if err := printHelp(env.stdout, o.lang, env.getenv); err != nil {
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitUsage
		}
		return exitOK
	case o.version:
		fmt.Fprintf(env.stdout, "jevtri %s\n", version)
		return exitOK
	}
	if len(rest) > 0 && rest[0] == "init" {
		return runInit(o, env)
	}
	if len(rest) == 1 && rest[0] == "report" {
		return runReport(o, env)
	}
	if len(rest) > 0 {
		fmt.Fprintf(env.stderr, "jevtri: unexpected argument %q\nRun 'jevtri --help' for usage.\n", rest[0])
		return exitUsage
	}

	cfg, err := config.Load(o.configPath)
	if errors.Is(err, os.ErrNotExist) {
		if env.isTerminal() && o.configPath == defaultConfPath {
			fmt.Fprintf(env.stderr, "jevtri: %s does not exist yet; starting first-time setup.\n", o.configPath)
			return runInit(o, env)
		}
		fmt.Fprintf(env.stderr, "jevtri: %s does not exist; run 'jevtri init' from a terminal first\n", o.configPath)
		return exitFailure
	}
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}

	minutes := cfg.Minutes
	if o.minutes != 0 {
		minutes = o.minutes
	}
	now := env.now()
	w, err := window.Parse(o.timeArgument, minutes, now)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitUsage
	}
	zone := now.Location()

	infos, collected := collect(cfg, w, zone, env.stderr)
	rep := report.Report{Reference: w.Reference, WindowStart: w.Start, WindowEnd: w.End, Minutes: minutes, Symptom: o.issue, Logs: infos, DryRun: o.dryRun}
	if len(collected) == 0 {
		if !anyQuiet(infos) {
			fmt.Fprintln(env.stderr, "jevtri: none of the configured logs could be evaluated; nothing was sent")
			return exitFailure
		}
		// Every readable log was quiet: a valid answer, but there is nothing to rank.
		rep.DryRun = false
		if code := printReport(rep, o, env); code != exitOK {
			return code
		}
		return partialOr(infos, exitOK)
	}

	selections := budget.Fit(collected, cfg.MaxBytes, w.Reference)
	query := jev.Query{Reference: w.Reference, WindowStart: w.Start, WindowEnd: w.End, Minutes: minutes, Symptom: o.issue}
	for _, selection := range selections {
		info := findInfo(infos, selection.Name)
		info.SentEntries, info.SentBytes, info.Dropped = len(selection.Entries), selection.Bytes, selection.Dropped
		query.Logs = append(query.Logs, jev.Log{Path: info.Path, Text: joinEntries(selection.Entries)})
	}
	request := jev.BuildRequest(query)

	if o.dryRun {
		record(cfg.SentLog, sentlog.Entry(now, request, true, nil, nil), env.stderr, &rep)
		encoder := json.NewEncoder(env.stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(request); err != nil {
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitFailure
		}
		if o.verbose {
			report.Text(env.stderr, rep, true)
		}
		return partialOr(infos, exitOK)
	}

	apiKey, err := config.LoadAPIKey(env.keyPath)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	// Nothing is sent that cannot be recorded afterwards (spec 12.2).
	if err := sentlog.CheckWritable(cfg.SentLog); err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\njevtri: nothing was sent, because the send is recorded there. Fix it, or use --dry-run.\n", err)
		return exitFailure
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := env.newAsker(apiKey).Ask(ctx, query)
	if err != nil {
		record(cfg.SentLog, sentlog.Entry(now, request, false, nil, err), env.stderr, &rep)
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitJevFailure
	}
	record(cfg.SentLog, sentlog.Entry(now, request, false, &result, nil), env.stderr, &rep)
	rep.Scores, rep.Model, rep.Latency, rep.InputTokens = result.Scores, result.Model, result.Latency, result.InputTokens
	if code := printReport(rep, o, env); code != exitOK {
		return code
	}
	return partialOr(infos, exitOK)
}

func printReport(rep report.Report, o options, env environment) int {
	if o.json {
		if err := report.JSON(env.stdout, rep); err != nil {
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	report.Text(env.stdout, rep, o.verbose)
	return exitOK
}

func anyQuiet(infos []report.LogInfo) bool {
	for _, info := range infos {
		if info.Quiet() {
			return true
		}
	}
	return false
}

// collect reads, masks and prepares every configured log. Logs that cannot
// be evaluated are reported, never ranked.
func collect(cfg *config.Config, w window.Window, zone *time.Location, stderr io.Writer) ([]report.LogInfo, []budget.Log) {
	var infos []report.LogInfo
	var logs []budget.Log
	for _, entry := range cfg.Logs {
		info := report.LogInfo{Name: entry.Name, Path: entry.Path, TimeFormat: entry.TimeFormat}
		if entry.Format == nil {
			info.Skipped = "time_format is not set; run 'jevtri init'"
			fmt.Fprintf(stderr, "jevtri: %s: %s\n", entry.Path, info.Skipped)
			infos = append(infos, info)
			continue
		}
		masker, err := mask.New(entry.Masks)
		if err != nil {
			info.Skipped = err.Error()
			fmt.Fprintf(stderr, "jevtri: %s: %s\n", entry.Path, info.Skipped)
			infos = append(infos, info)
			continue
		}
		result, err := logread.Read(logread.Source{
			Name: entry.Name, Path: entry.Path, Format: entry.Format, Location: entry.Location, ReadCompressed: entry.ReadCompressed,
		}, w, zone)
		if err != nil {
			info.Skipped = unwrapReason(err)
			fmt.Fprintf(stderr, "jevtri: %s: %s\n", entry.Path, info.Skipped)
			infos = append(infos, info)
			continue
		}
		info.Entries, info.Files, info.BytesRead = len(result.Entries), result.Files, result.BytesRead
		info.ReadTruncated = result.Truncated
		if result.Truncated {
			fmt.Fprintf(stderr, "jevtri: warning: %s: part of the window was left out while reading (size limits)\n", entry.Path)
		}
		info.Masked = map[string]int{}
		log := budget.Log{Name: entry.Name}
		for _, e := range result.Entries {
			masked := masker.Apply(e.Text)
			for kind, count := range masked.Counts {
				info.Masked[kind] += count
			}
			log.Entries = append(log.Entries, budget.Entry{Time: e.Time, Text: masked.Text})
		}
		infos = append(infos, info)
		if len(log.Entries) > 0 {
			logs = append(logs, log) // a quiet log is not sent
		}
	}
	return infos, logs
}

func unwrapReason(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func findInfo(infos []report.LogInfo, name string) *report.LogInfo {
	for i := range infos {
		if infos[i].Name == name {
			return &infos[i]
		}
	}
	panic("unknown log " + name)
}

func joinEntries(entries []budget.Entry) string {
	var text []byte
	for i, entry := range entries {
		if i > 0 {
			text = append(text, '\n')
		}
		text = append(text, entry.Text...)
	}
	return string(text)
}

// record writes the send log. CheckWritable has already run before a send, so
// a failure here means the file became unwritable during the run; the logs are
// out and the record is not, which is said plainly.
func record(path string, entry sentlog.Record, stderr io.Writer, rep *report.Report) {
	if err := sentlog.Append(path, entry); err != nil {
		fmt.Fprintf(stderr, "jevtri: warning: %v\njevtri: warning: this run is NOT in the send log\n", err)
		return
	}
	rep.SentLog = path
}

func partialOr(infos []report.LogInfo, code int) int {
	for _, info := range infos {
		if info.Skipped != "" {
			return exitPartial
		}
	}
	return code
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// runInit writes the configuration interactively (spec O-05). It needs a
// person at a terminal, so cron and pipes get an error instead.
func runInit(o options, env environment) int {
	if !env.isTerminal() {
		fmt.Fprintln(env.stderr, "jevtri: init asks questions and needs a terminal")
		return exitFailure
	}
	err := setup.Run(env.stdin, env.stdout, setup.Options{
		Root:        env.root,
		ConfigPath:  o.configPath,
		Now:         env.now(),
		LoadAPIKey:  func() (string, error) { return config.LoadAPIKey(env.keyPath) },
		NewDetector: func(key string) setup.Detector { return env.newAsker(key) },
	})
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	return exitOK
}
