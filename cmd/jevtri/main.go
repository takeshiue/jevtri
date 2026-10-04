// Command jevtri ranks server logs by how worth they are to examine first
// during an incident, using Jev.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/budget"
	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/containers"
	"github.com/takeshiue/jevtri/internal/grouprank"
	"github.com/takeshiue/jevtri/internal/jev"
	"github.com/takeshiue/jevtri/internal/journal"
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
var version = "0.3.0"

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
	root           string // prefix for Docker's and journalctl's paths and what init looks at; tests only
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
	timeArgument  string
	minutes       int
	issue         string
	configPath    string
	verbose       bool
	json          bool
	dryRun        bool
	help          bool
	version       bool
	configUpdate  bool
	show          bool
	showConflicts []string
	lang          string
	groups        []string
	allGroups     bool
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
	fs.BoolVar(&o.configUpdate, "config-update", false, "")
	fs.BoolVar(&o.show, "show", false, "")
	fs.BoolVar(&o.allGroups, "all-groups", false, "")
	fs.Func("group", "", func(value string) error {
		names, err := parseGroupNames(value)
		if err != nil {
			return err
		}
		for _, name := range names {
			found := false
			for _, existing := range o.groups {
				if existing == name {
					found = true
					break
				}
			}
			if !found {
				o.groups = append(o.groups, name)
			}
		}
		return nil
	})
	fs.StringVar(&o.lang, "lang", "", "")
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	if o.show {
		fs.Visit(func(option *flag.Flag) {
			switch option.Name {
			case "c", "config", "show", "j", "json", "lang", "h", "help", "version":
			default:
				o.showConflicts = append(o.showConflicts, "--"+option.Name)
			}
		})
	}
	for _, argument := range fs.Args() {
		if argument == "--show" || strings.HasPrefix(argument, "--show=") {
			return o, nil, fmt.Errorf("--show cannot be used with a command")
		}
	}
	if o.allGroups && len(o.groups) > 0 {
		return o, nil, fmt.Errorf("--all-groups cannot be used with --group")
	}
	return o, fs.Args(), nil
}

func parseGroupNames(value string) ([]string, error) {
	names := strings.Split(value, ",")
	for index, name := range names {
		name = strings.TrimSpace(name)
		if !config.GroupName.MatchString(name) {
			return nil, fmt.Errorf("--group must contain group names separated by commas (letters, digits, _ . -); empty names are not allowed")
		}
		names[index] = name
	}
	return names, nil
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
	if o.show {
		if len(rest) != 0 || len(o.showConflicts) != 0 {
			fmt.Fprintln(env.stderr, "jevtri: --show cannot be combined with analysis options or commands")
			return exitUsage
		}
		return runShow(o, env)
	}
	if o.configUpdate {
		if len(rest) > 0 {
			fmt.Fprintf(env.stderr, "jevtri: unexpected argument %q\nRun 'jevtri --help' for usage.\n", rest[0])
			return exitUsage
		}
		return runConfigUpdate(o, env)
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

	warningLanguage := "en"
	if env.getenv != nil {
		warningLanguage, _ = helpLanguage("", env.getenv)
	}
	infos, collected, groups, err := collect(cfg, w, zone, env.root, env.stderr, warningLanguage)
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	knownGroups := map[string]bool{grouprank.System: true}
	for _, assigned := range groups {
		for _, group := range assigned {
			knownGroups[group] = true
		}
	}
	for _, group := range o.groups {
		if !knownGroups[group] {
			fmt.Fprintf(env.stderr, "jevtri: unknown group %q\n", group)
			return exitUsage
		}
	}
	var identities jev.Query
	for _, info := range infos {
		identities.Logs = append(identities.Logs, jev.Log{Path: info.Path})
	}
	if err := jev.ValidateQuery(identities); err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	rep := report.Report{Reference: w.Reference, WindowStart: w.Start, WindowEnd: w.End, Minutes: minutes, Symptom: o.issue, Logs: infos, DryRun: o.dryRun}
	if o.allGroups {
		var registered []grouprank.Member
		for name, assigned := range groups {
			registered = append(registered, grouprank.Member{Name: name, Groups: assigned})
		}
		rep.Examined = grouprank.Names(registered)
	} else {
		rep.Examined = o.groups
	}
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

	members := make([]grouprank.Member, len(collected))
	for i, log := range collected {
		members[i] = grouprank.Member{Name: log.Name, Groups: groups[log.Name]}
	}
	if len(o.groups) > 0 {
		for name, assigned := range groups {
			active := false
			for _, log := range collected {
				if log.Name == name {
					active = true
					break
				}
			}
			if !active {
				members = append(members, grouprank.Member{Name: name, Groups: assigned})
			}
		}
	}
	var plan grouprank.Plan
	if o.allGroups {
		for _, member := range members {
			plan.Direct = append(plan.Direct, member.Name)
		}
	} else {
		plan, err = grouprank.Make(members, o.groups)
		if err != nil {
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitUsage
		}
	}

	// The asker is made once, before the first send, after the checks that
	// must pass before anything leaves the host.
	var ask asker
	prepare := func() int {
		if ask != nil {
			return exitOK
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
		ask = env.newAsker(apiKey)
		return exitOK
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ranked := plan.Direct
	if plan.FirstStage {
		query, firstSelections, err := groupQuery(plan, collected, infos, cfg.MaxBytes, w, minutes, o.issue)
		if err != nil {
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitFailure
		}
		for _, selection := range firstSelections {
			if selection.Dropped > 0 {
				fmt.Fprintf(env.stderr, "jevtri: first stage: %s: %d entries left out by max_bytes\n", findInfo(infos, selection.Name).Path, selection.Dropped)
			}
		}
		request := jev.BuildRequest(query)
		if o.dryRun {
			// The second stage depends on Jev's answer, so only the first is shown.
			fmt.Fprintln(env.stderr, "jevtri: this is the first stage (which group to examine); the second stage depends on Jev's answer")
			return printDryRun(request, rep, cfg, now, o, env, infos)
		}
		if code := prepare(); code != exitOK {
			return code
		}
		result, err := ask.Ask(ctx, query)
		if err != nil {
			recordGroup(cfg.SentLog, sentlog.Entry(now, request, false, nil, err), env.stderr, &rep)
			fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
			return exitJevFailure
		}
		if !recordGroup(cfg.SentLog, sentlog.Entry(now, request, false, &result, nil), env.stderr, &rep) {
			fmt.Fprintln(env.stderr, "jevtri: first stage was sent but could not be recorded; no further requests will be sent")
			return exitFailure
		}
		for _, selection := range firstSelections {
			info := findInfo(infos, selection.Name)
			info.FirstStageEntries += len(selection.Entries)
			info.FirstStageBytes += len(joinEntries(selection.Entries))
		}
		rep.GroupScores = result.Scores
		var firstStage []grouprank.Score
		for _, score := range jev.Ranked(result.Scores) {
			firstStage = append(firstStage, grouprank.Score{Group: strings.TrimPrefix(score.Path, "group "), Priority: score.Priority})
		}
		chosen := grouprank.Choose(firstStage)
		rep.Examined = chosen
		ranked = grouprank.Second(plan, chosen)
	}
	keep := map[string]bool{}
	for _, name := range ranked {
		keep[name] = true
	}
	var examined []budget.Log
	for _, log := range collected {
		if keep[log.Name] {
			examined = append(examined, log)
		} else {
			findInfo(infos, log.Name).OtherGroup = true
		}
	}

	if len(examined) == 0 {
		rep.DryRun = false
		if code := printReport(rep, o, env); code != exitOK {
			return code
		}
		return partialOr(infos, exitOK)
	}
	query, selections, err := fitState(cfg.MaxBytes, func(limit int) (jev.Query, []budget.Selection) {
		query := jev.Query{Reference: w.Reference, WindowStart: w.Start, WindowEnd: w.End, Minutes: minutes, Symptom: o.issue}
		selections := budget.Fit(examined, limit, w.Reference)
		for _, selection := range selections {
			query.Logs = append(query.Logs, jev.Log{Path: findInfo(infos, selection.Name).Path, Text: joinEntries(selection.Entries)})
		}
		return query, selections
	})
	if err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}
	var nonempty []jev.Log
	for _, log := range query.Logs {
		if log.Text != "" {
			nonempty = append(nonempty, log)
		}
	}
	query.Logs = nonempty
	if len(query.Logs) == 0 {
		fmt.Fprintln(env.stderr, "jevtri: max_bytes leaves no log excerpts to evaluate; nothing was sent in this stage")
		return exitFailure
	}
	for _, selection := range selections {
		info := findInfo(infos, selection.Name)
		info.SentEntries, info.SentBytes, info.Dropped = len(selection.Entries), len(joinEntries(selection.Entries)), selection.Dropped
	}
	if err := jev.ValidateQuery(query); err != nil {
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitFailure
	}

	request := jev.BuildRequest(query)

	if o.dryRun {
		return printDryRun(request, rep, cfg, now, o, env, infos)
	}
	if code := prepare(); code != exitOK {
		return code
	}
	result, err := ask.Ask(ctx, query)
	if err != nil {
		record(cfg.SentLog, sentlog.Entry(now, request, false, nil, err), env.stderr, &rep)
		fmt.Fprintf(env.stderr, "jevtri: %v\n", err)
		return exitJevFailure
	}
	recorded := record(cfg.SentLog, sentlog.Entry(now, request, false, &result, nil), env.stderr, &rep)
	rep.Scores, rep.Model, rep.Latency, rep.InputTokens = result.Scores, result.Model, result.Latency, result.InputTokens
	if code := printReport(rep, o, env); code != exitOK {
		return code
	}
	if !recorded {
		return exitFailure
	}
	return partialOr(infos, exitOK)
}

// printDryRun records and prints the request that would be sent.
func printDryRun(request jev.Request, rep report.Report, cfg *config.Config, now time.Time, o options, env environment, infos []report.LogInfo) int {
	recorded := record(cfg.SentLog, sentlog.Entry(now, request, true, nil, nil), env.stderr, &rep)
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
	if !recorded {
		return exitFailure
	}
	return partialOr(infos, exitOK)
}

// groupQuery is the first stage (spec 12.7.4): one entry per service group,
// holding its logs' excerpts, each headed by the log's name. The send limit
// is shared equally between the groups, then within a group between its logs.
func groupQuery(plan grouprank.Plan, collected []budget.Log, infos []report.LogInfo, maxBytes int, w window.Window, minutes int, issue string) (jev.Query, []budget.Selection, error) {
	byName := map[string]budget.Log{}
	for _, log := range collected {
		byName[log.Name] = log
	}
	names := plan.CandidateNames()
	if len(names) == 0 {
		return jev.Query{}, nil, fmt.Errorf("no active groups to evaluate")
	}
	query, selections, err := fitState(maxBytes, func(limit int) (jev.Query, []budget.Selection) {
		query := jev.Query{Reference: w.Reference, WindowStart: w.Start, WindowEnd: w.End, Minutes: minutes, Symptom: issue, Groups: true}
		var allSelections []budget.Selection
		for _, group := range names {
			var logs []budget.Log
			for _, name := range plan.Candidates[group] {
				logs = append(logs, byName[name])
			}
			var text strings.Builder
			selected := budget.Fit(logs, limit/len(names), w.Reference)
			allSelections = append(allSelections, selected...)
			for _, selection := range selected {
				if len(selection.Entries) == 0 {
					continue
				}
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				fmt.Fprintf(&text, "== %s ==\n%s", findInfo(infos, selection.Name).Path, joinEntries(selection.Entries))
			}
			query.Logs = append(query.Logs, jev.Log{Path: "group " + group, Text: text.String()})
		}
		return query, allSelections
	})
	if err != nil {
		return query, selections, err
	}
	var active []jev.Log
	for _, log := range query.Logs {
		if log.Text != "" {
			active = append(active, log)
		}
	}
	query.Logs = active
	if len(active) == 0 {
		return query, selections, fmt.Errorf("max_bytes leaves no group excerpts to evaluate; nothing was sent")
	}
	if err := jev.ValidateQuery(query); err != nil {
		return query, selections, err
	}
	return query, selections, nil
}

// fitState measures the serialized state, including JSON escaping and metadata.
// Every accepted candidate is measured directly; raw text bytes cannot predict
// the serialized size.
func fitState(maxBytes int, build func(int) (jev.Query, []budget.Selection)) (jev.Query, []budget.Selection, error) {
	query, selections := build(0)
	state, err := json.Marshal(jev.BuildRequest(query).State)
	if err != nil {
		return query, selections, err
	}
	if len(state) > maxBytes {
		return query, selections, fmt.Errorf("max_bytes %d is smaller than state metadata (%d bytes)", maxBytes, len(state))
	}
	best, bestSelections := query, selections
	low, high := 0, maxBytes
	for low <= high {
		limit := low + (high-low)/2
		candidate, selected := build(limit)
		state, err := json.Marshal(jev.BuildRequest(candidate).State)
		if err != nil {
			return candidate, selected, err
		}
		if len(state) <= maxBytes {
			best, bestSelections = candidate, selected
			low = limit + 1
		} else {
			high = limit - 1
		}
	}
	return best, bestSelections, nil
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
// be evaluated are reported, never ranked. root prefixes Docker's and
// journalctl's locations (tests only).
func collect(cfg *config.Config, w window.Window, zone *time.Location, root string, stderr io.Writer, warningLanguage string) ([]report.LogInfo, []budget.Log, map[string][]string, error) {
	var infos []report.LogInfo
	var logs []budget.Log
	groups := map[string][]string{}
	if err := containers.ValidateNames(cfg.Logs); err != nil {
		return nil, nil, nil, err
	}
	// Legacy sources must be upgraded before any log is read or sent.
	for _, entry := range cfg.Logs {
		if entry.Path == "" && (entry.DockerContainer != "" || entry.DockerProject != "") {
			return nil, nil, nil, fmt.Errorf("[log %s] has no registered Docker log path; run 'jevtri --config-update' to verify and register its path", entry.Name)
		}
	}
	missingGroups := 0
	for _, entry := range cfg.Logs {
		if len(entry.Groups) == 0 {
			missingGroups++
		}
	}
	if missingGroups > 0 {
		fmt.Fprintf(stderr, "jevtri: warning: %d configured logs have no group and are included in every selection; run 'jevtri --config-update' to assign groups\n", missingGroups)
	}
	for _, entry := range cfg.Logs {
		groups[entry.Name] = entry.Groups
		info := report.LogInfo{Name: entry.Name, Path: entry.Label(), TimeFormat: entry.TimeFormat}
		skip := func(reason string) {
			info.Skipped = reason
			fmt.Fprintf(stderr, "jevtri: %s: %s\n", info.Path, reason)
			infos = append(infos, info)
		}
		if entry.Format == nil {
			skip("time_format is not set; run 'jevtri init'")
			continue
		}
		masker, err := mask.New(entry.Masks)
		if err != nil {
			skip(err.Error())
			continue
		}
		result, err := readLog(entry, w, zone, root)
		if err != nil {
			if registeredLogMissing(entry) {
				info.Skipped = printMissingLogWarning(stderr, entry.Path, warningLanguage)
				infos = append(infos, info)
			} else {
				skip(unwrapReason(err))
			}
			continue
		}
		info.Entries, info.Files, info.BytesRead = len(result.Entries), result.Files, result.BytesRead
		info.ReadTruncated = result.Truncated
		if result.Truncated {
			fmt.Fprintf(stderr, "jevtri: warning: %s: part of the window was left out while reading (size limits)\n", info.Path)
		}
		info.Masked = result.Masked
		if info.Masked == nil {
			info.Masked = map[string]int{}
		}
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
			groups[entry.Name] = entry.Groups
		}
	}
	return infos, logs, groups, nil
}

func registeredLogMissing(entry config.Log) bool {
	if entry.Path == "" {
		return false
	}
	if logread.IsPattern(entry.Path) {
		matches, err := filepath.Glob(entry.Path)
		return err == nil && len(matches) == 0
	}
	_, err := os.Lstat(entry.Path)
	return errors.Is(err, os.ErrNotExist)
}

func printMissingLogWarning(stderr io.Writer, path, language string) string {
	switch language {
	case "ja":
		fmt.Fprintf(stderr, "警告: ログファイルが見つかりません: %s\nこのログをスキップしました。\n設定を確認更新するには 'jevtri --config-update' を実行してください。\n", path)
		return "ログファイルが見つかりません"
	case "zh-CN":
		fmt.Fprintf(stderr, "警告: 找不到日志文件: %s\n已跳过此日志。\n请运行 'jevtri --config-update' 检查并更新配置。\n", path)
		return "找不到日志文件"
	default:
		fmt.Fprintf(stderr, "Warning: Log file not found: %s\nThis log was skipped.\nRun 'jevtri --config-update' to check and update your config.\n", path)
		return "Log file not found"
	}
}

// journalLimit caps the journalctl output read for one log, like the text
// kept for one file.
const journalLimit = 64 << 20

// readLog reads one configured log from its file, its Docker container's
// file or journalctl (spec 12.7).
func readLog(entry config.Log, w window.Window, zone *time.Location, root string) (logread.Result, error) {
	source := logread.Source{Name: entry.Name, Path: entry.Path, Format: entry.Format, Location: entry.Location, ReadCompressed: entry.ReadCompressed}
	switch {
	case entry.JournalUnit != "":
		journalctl, err := journal.Find(root)
		if err != nil {
			return logread.Result{Source: source}, err
		}
		output, truncated, err := journal.Output(journalctl, entry.JournalUnit, w.Start, w.End, journalLimit)
		if err != nil {
			return logread.Result{Source: source}, err
		}
		result, err := logread.ReadStream(bytes.NewReader(output), source, w, zone)
		result.Truncated = result.Truncated || truncated
		result.Files = []string{journalctl + " " + strings.Join(journal.Args(entry.JournalUnit, w.Start, w.End), " ")}
		return result, err
	}
	return logread.Read(source, w, zone)
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
func recordGroup(path string, entry sentlog.Record, stderr io.Writer, rep *report.Report) bool {
	entry.Purpose = "group_ranking"
	return record(path, entry, stderr, rep)
}

func record(path string, entry sentlog.Record, stderr io.Writer, rep *report.Report) bool {
	if err := sentlog.Append(path, entry); err != nil {
		fmt.Fprintf(stderr, "jevtri: warning: %v\njevtri: warning: this run is NOT in the send log\n", err)
		return false
	}
	rep.SentLog = path
	return true
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
		KeyPath:     env.keyPath,
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

// runConfigUpdate adds logs found since the configuration was written (spec
// 12.7.3). Like init it asks questions, so it needs a terminal.
func runConfigUpdate(o options, env environment) int {
	if !env.isTerminal() {
		fmt.Fprintln(env.stderr, "jevtri: --config-update asks questions and needs a terminal")
		return exitFailure
	}
	err := setup.Update(env.stdin, env.stdout, setup.Options{
		Root:        env.root,
		ConfigPath:  o.configPath,
		KeyPath:     env.keyPath,
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
