package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/logread"
	"github.com/takeshiue/jevtri/internal/safeopen"
	"github.com/takeshiue/jevtri/internal/timefmt"
)

// Options are the parts of the host that tests replace.
type Options struct {
	// Root is prepended to every path that is looked at; empty on a real host.
	Root string
	// ConfigPath is where the configuration is written.
	ConfigPath string
	Now        time.Time
	// LoadAPIKey and NewDetector reach Jev for logs whose format is not
	// known; nil means Jev is not used.
	LoadAPIKey  func() (string, error)
	NewDetector func(apiKey string) Detector
	// SentLog records what was sent to Jev (spec 12.2).
	SentLog string
}

// Candidate is a log that may go into the configuration.
type Candidate struct {
	Name       string
	Path       string // as written to the configuration (a glob for dated logs)
	TimeFormat string // empty when no known format reads the file
	// Assumed is set when the log and its rotated files were empty, so
	// TimeFormat is the usual format of that location, not a checked one.
	Assumed bool
}

// ErrExists is returned when the configuration file appears while init is
// writing it; init never overwrites a configuration.
var ErrExists = errors.New("configuration file already exists")

const notSetComment = "# time_format is not set: this log is skipped until it is."

// DetectFamily reads ID and ID_LIKE of an os-release file.
func DetectFamily(osRelease string) Family {
	var words []string
	for _, line := range strings.Split(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && (key == "ID" || key == "ID_LIKE") {
			words = append(words, strings.Fields(strings.Trim(value, `"'`))...)
		}
	}
	for _, word := range words {
		switch word {
		case "rhel", "fedora", "centos", "almalinux", "rocky":
			return FamilyRHEL
		case "debian", "ubuntu":
			return FamilyDebian
		}
	}
	return FamilyUnknown
}

func (f Family) String() string {
	switch f {
	case FamilyRHEL:
		return "RHEL family"
	case FamilyDebian:
		return "Debian family"
	}
	return "unknown family"
}

// Find returns the known logs that exist and can be read, the given
// family's locations first (spec 12.4).
func Find(family Family, root string) []Candidate {
	type place struct {
		name    string
		path    string
		formats []string
		dated   bool
	}
	var places []place
	add := func(family Family) {
		for _, k := range knownLogs {
			paths, formats := k.RHEL, k.RHELFormats
			if family == FamilyDebian {
				paths, formats = k.Debian, k.DebianFormats
			}
			for _, path := range paths {
				places = append(places, place{name: k.Name, path: path, formats: formats, dated: contains(k.Dated, path)})
			}
		}
	}
	if family == FamilyDebian {
		add(FamilyDebian)
		add(FamilyRHEL)
	} else {
		add(FamilyRHEL)
		add(FamilyDebian)
	}

	var found []Candidate
	seenPath := map[string]bool{}
	seenName := map[string]int{}
	appendCandidate := func(name, path string, formats []string, sample string) {
		if seenPath[path] {
			return
		}
		seenPath[path] = true
		seenName[name]++
		if seenName[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, seenName[name])
		}
		format, assumed := detectFormat(sample, formats)
		found = append(found, Candidate{Name: name, Path: path, TimeFormat: format, Assumed: assumed})
	}
	for _, p := range places {
		if !logread.IsPattern(p.path) {
			if readable(filepath.Join(root, p.path)) {
				appendCandidate(p.name, p.path, p.formats, filepath.Join(root, p.path))
			}
			continue
		}
		matches := readableMatches(root, p.path)
		if len(matches) == 0 {
			continue
		}
		if p.dated {
			appendCandidate(p.name, p.path, p.formats, filepath.Join(root, matches[0]))
			continue
		}
		for _, match := range matches {
			name := p.name
			if len(matches) > 1 {
				name = p.name + "-" + stem(match)
			}
			appendCandidate(name, match, p.formats, filepath.Join(root, match))
		}
	}
	return found
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func readable(path string) bool {
	// A file name with a newline would inject lines into the configuration, and
	// an escape sequence would be written to the terminal (SEC-004, SEC-005).
	if strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	handle, err := safeopen.Open(path)
	if err != nil {
		return false
	}
	handle.Close()
	return true
}

// readableMatches returns the readable files matching pattern (without the
// root), newest first, skipping rotated and compressed ones.
func readableMatches(root, pattern string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, pattern))
	type match struct {
		path     string
		modified time.Time
	}
	var list []match
	for _, m := range matches {
		if strings.HasSuffix(m, ".gz") || !readable(m) {
			continue
		}
		info, _ := os.Stat(m)
		list = append(list, match{path: strings.TrimPrefix(m, root), modified: info.ModTime()})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].modified.After(list[j].modified) })
	paths := make([]string, len(list))
	for i, m := range list {
		paths[i] = m.path
	}
	return paths
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// stem turns /var/log/sssd/sssd_nss.log into sssd_nss, usable in [log NAME].
func stem(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".log")
	name = unsafeName.ReplaceAllString(name, "-")
	if name == "" {
		return "log"
	}
	return name
}

// tailSize is how much of the end of a log is sampled to check a format.
const tailSize = 64 * 1024

// detectFormat returns the first of formats that finds a timestamp in the
// recent lines of the file, or "" when none does. Only the formats known
// for this location are tried; anything else is left to Jev (I-02) or to
// the user, because trying the whole catalog would accept false matches
// such as "epoch" on any ten-digit number.
//
// A log that was just rotated is empty, so its newest rotated file is
// sampled instead. When everything is empty the usual format is assumed:
// if it is wrong, the run reports "no timestamps found" and sends nothing.
func detectFormat(path string, formats []string) (string, bool) {
	lines := tailLines(path)
	if blank(lines) {
		lines = tailLines(newestRotated(path))
	}
	if blank(lines) {
		if len(formats) > 0 {
			return formats[0], true
		}
		return "", false
	}
	for _, name := range formats {
		format, err := timefmt.Lookup(name)
		if err != nil {
			continue
		}
		for _, line := range lines {
			if _, ok := format.Find(line, time.Local); ok {
				return name, false
			}
		}
	}
	return "", false
}

func blank(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

// newestRotated returns the most recently modified uncompressed rotated
// file of path (messages-20260927, syslog.1), or "" if there is none.
func newestRotated(path string) string {
	best, bestTime := "", time.Time{}
	for _, pattern := range []string{path + ".[0-9]*", path + "-[0-9]*"} {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || !info.Mode().IsRegular() || strings.HasSuffix(match, ".gz") {
				continue
			}
			if info.ModTime().After(bestTime) {
				best, bestTime = match, info.ModTime()
			}
		}
	}
	return best
}

func tailLines(path string) []string {
	handle, err := safeopen.Open(path)
	if err != nil {
		return nil
	}
	defer handle.Close()
	if info, err := handle.Stat(); err == nil && info.Size() > tailSize {
		handle.Seek(info.Size()-tailSize, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(handle, tailSize))
	return strings.Split(string(data), "\n")
}

// ParseSelection turns "1 3-4" (or "1,3-4") into indexes 0..count-1.
// An empty answer selects everything.
func ParseSelection(answer string, count int) ([]int, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		all := make([]int, count)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	chosen := map[int]bool{}
	for _, field := range strings.FieldsFunc(answer, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
		low, high, isRange := strings.Cut(field, "-")
		if !isRange {
			high = low
		}
		first, err1 := strconv.Atoi(low)
		last, err2 := strconv.Atoi(high)
		if err1 != nil || err2 != nil || first < 1 || last > count || first > last {
			return nil, fmt.Errorf("%q is not a number or range between 1 and %d", field, count)
		}
		for n := first; n <= last; n++ {
			chosen[n-1] = true
		}
	}
	var indexes []int
	for i := 0; i < count; i++ {
		if chosen[i] {
			indexes = append(indexes, i)
		}
	}
	return indexes, nil
}

// Run asks the user which logs to use and writes the configuration.
func Run(in io.Reader, out io.Writer, opts Options) error {
	reader := bufio.NewReader(in)
	if _, err := os.Stat(opts.ConfigPath); err == nil {
		return completeExisting(reader, out, opts)
	}
	osRelease, _ := os.ReadFile(filepath.Join(opts.Root, "/etc/os-release"))
	family := DetectFamily(string(osRelease))
	candidates := Find(family, opts.Root)
	if opts.SentLog == "" {
		opts.SentLog = config.DefaultSentLog
	}

	fmt.Fprintf(out, "Looking for logs (%s).\n", family)
	var selected []Candidate
	if len(candidates) == 0 {
		fmt.Fprintln(out, "No known log was found.")
	} else {
		fmt.Fprintf(out, "Found %d logs:\n", len(candidates))
		width := 0
		for _, c := range candidates {
			width = max(width, len(c.Path))
		}
		for i, c := range candidates {
			format := c.TimeFormat
			switch {
			case format == "":
				format = "time format unknown"
			case c.Assumed:
				format += ", assumed: the log is empty"
			}
			fmt.Fprintf(out, "  %2d. %-*s  (%s)\n", i+1, width, c.Path, format)
		}
		for {
			fmt.Fprintf(out, "Logs to use (e.g. \"1 3-4\"; empty for all): ")
			answer, err := reader.ReadString('\n')
			if err != nil && answer == "" {
				return errors.New("no answer; nothing was written")
			}
			indexes, perr := ParseSelection(answer, len(candidates))
			if perr != nil {
				fmt.Fprintf(out, "  %v\n", perr)
				continue
			}
			for _, i := range indexes {
				selected = append(selected, candidates[i])
			}
			break
		}
	}

	names := map[string]bool{}
	for _, c := range selected {
		names[c.Name] = true
	}
	fmt.Fprintln(out, "Other log paths to add, one per line (empty line to finish):")
	for {
		fmt.Fprint(out, "> ")
		line, err := reader.ReadString('\n')
		path := strings.TrimSpace(line)
		if path == "" {
			break
		}
		switch {
		case !filepath.IsAbs(path):
			fmt.Fprintln(out, "  give an absolute path")
		case logread.IsPattern(path) && len(readableMatches(opts.Root, path)) == 0:
			fmt.Fprintln(out, "  no readable file matches it")
		case !logread.IsPattern(path) && !readable(filepath.Join(opts.Root, path)):
			fmt.Fprintln(out, "  not a readable file")
		default:
			name := stem(path)
			for n := 2; names[name]; n++ {
				name = fmt.Sprintf("%s-%d", stem(path), n)
			}
			names[name] = true
			selected = append(selected, Candidate{Name: name, Path: path})
		}
		if err != nil {
			break
		}
	}
	if len(selected) == 0 {
		return errors.New("no log was selected; nothing was written")
	}
	var unknownFormats []*Candidate
	for i := range selected {
		if selected[i].TimeFormat == "" {
			unknownFormats = append(unknownFormats, &selected[i])
		}
	}
	detectWithJev(reader, out, unknownFormats, opts)

	if err := write(opts.ConfigPath, Render(selected, opts.Now)); err != nil {
		return err
	}
	noun := "logs"
	if len(selected) == 1 {
		noun = "log"
	}
	fmt.Fprintf(out, "Wrote %s with %d %s.\n", opts.ConfigPath, len(selected), noun)
	var unknown []string
	for _, c := range selected {
		if c.TimeFormat == "" {
			unknown = append(unknown, c.Path)
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintln(out, "These logs have no time_format yet and are skipped until it is set:")
		for _, path := range unknown {
			fmt.Fprintf(out, "  - %s\n", path)
		}
		fmt.Fprintln(out, "Write time_format for them in the file (a catalog name or % directives; see jevtri(1)).")
	}
	return nil
}

// completeExisting is 'jevtri init' on an existing configuration: it looks
// for the time format of logs added without one (spec O-05) and changes
// nothing else.
func completeExisting(reader *bufio.Reader, out io.Writer, opts Options) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}
	if opts.SentLog == "" {
		opts.SentLog = cfg.SentLog
	}
	var missing []*Candidate
	for _, log := range cfg.Logs {
		if log.TimeFormat == "" {
			missing = append(missing, &Candidate{Name: log.Name, Path: log.Path})
		}
	}
	if len(missing) == 0 {
		fmt.Fprintf(out, "%s exists and every log in it has a time_format; nothing to do.\n", opts.ConfigPath)
		fmt.Fprintln(out, "To add a log, add a [log NAME] section with its path and run 'jevtri init' again.")
		return nil
	}
	detectWithJev(reader, out, missing, opts)
	formats := map[string]string{}
	for _, c := range missing {
		if c.TimeFormat != "" {
			formats[c.Name] = c.TimeFormat
		}
	}
	if len(formats) == 0 {
		fmt.Fprintf(out, "%s was not changed.\n", opts.ConfigPath)
		return nil
	}
	if err := updateTimeFormats(opts.ConfigPath, formats); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added time_format for %d of %d logs to %s.\n", len(formats), len(missing), opts.ConfigPath)
	return nil
}

// Render writes the configuration text.
func Render(logs []Candidate, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# jevtri configuration, written by 'jevtri init' on %s.\n", now.Format("2006-01-02"))
	b.WriteString("# See jevtri(1). Lines starting with # are comments.\n\n")
	b.WriteString("[general]\n# minutes = 5\n# max_bytes = 48000\n# sent_log = /var/log/jevtri/sent.log\n")
	for _, c := range logs {
		fmt.Fprintf(&b, "\n[log %s]\npath = %s\n", c.Name, c.Path)
		if c.Assumed {
			b.WriteString("# The log was empty when this was written; time_format is the usual one, not a checked one.\n")
		}
		if c.TimeFormat != "" {
			fmt.Fprintf(&b, "time_format = %s\n", c.TimeFormat)
		} else {
			b.WriteString(notSetComment + "\n")
		}
	}
	return b.String()
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %v", filepath.Dir(path), err)
	}
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: %s", ErrExists, path)
	}
	if err != nil {
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if _, err := handle.WriteString(content); err != nil {
		handle.Close()
		// A half-written configuration would be found by the next init and fail
		// to parse, so leave nothing behind.
		os.Remove(path)
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		os.Remove(path)
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	if err := handle.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("cannot write %s: %v", path, err)
	}
	return nil
}
