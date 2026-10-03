// Package casereport turns one past run from the send log into a report of
// the real cause, for the public issue form (spec 12.6). The report is only
// written to a local file; the operator reviews it and posts it themselves.
package casereport

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/safeopen"
)

// IssueURL is the issue form of the public repository.
const IssueURL = "https://github.com/takeshiue/jevtri/issues/new"

// IssueTemplate is the file name of the form in .github/ISSUE_TEMPLATE/.
const IssueTemplate = "case.yml"

// MaxIssueBody is GitHub's limit on the body of an issue, in characters.
const MaxIssueBody = 65536

// maxLine bounds one line of the send log; a ranking request is at most the
// send budget plus the question texts.
const maxLine = 16 << 20

// Run is one ranking that reached Jev.
type Run struct {
	Time     time.Time
	Symptom  string
	Window   string
	Model    string
	Logs     map[string]string // path -> masked text that was sent
	Rankings []Ranking         // highest first
}

// Ranking is one log's result.
type Ranking struct {
	Path     string
	Priority int // 0..100, as shown by jevtri
}

type record struct {
	Time    string `json:"time"`
	Purpose string `json:"purpose"`
	DryRun  bool   `json:"dry_run"`
	Request struct {
		State struct {
			Task        string            `json:"task"`
			WindowStart string            `json:"window_start"`
			WindowEnd   string            `json:"window_end"`
			Symptom     string            `json:"symptom"`
			Logs        map[string]string `json:"logs"`
		} `json:"state"`
	} `json:"request"`
	Model  string `json:"model"`
	Scores []struct {
		Log   string  `json:"log"`
		Score float64 `json:"score"`
	} `json:"scores"`
	Error string `json:"error"`
}

// LoadRuns reads the send log and its rotated files (sent.log.1,
// sent.log.2.gz, ...) and returns the rankings that got an answer, newest
// first. Dry runs, failures and jevtri init questions have nothing to report.
func LoadRuns(sentLog string) ([]Run, error) {
	files, err := filepath.Glob(sentLog + "*")
	if err != nil {
		return nil, err
	}
	rotated := regexp.MustCompile(`^(\.\d+(\.gz)?)?$`)
	var runs []Run
	found := false
	for _, file := range files {
		if !rotated.MatchString(strings.TrimPrefix(file, sentLog)) {
			continue
		}
		found = true
		loaded, err := loadFile(file)
		if err != nil {
			return nil, err
		}
		runs = append(runs, loaded...)
	}
	if !found {
		return nil, fmt.Errorf("the send log %s does not exist", sentLog)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Time.After(runs[j].Time) })
	return runs, nil
}

func loadFile(path string) ([]Run, error) {
	handle, err := safeopen.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the send log: %v", err)
	}
	defer handle.Close()
	var reader io.Reader = handle
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(handle)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %v", path, err)
		}
		defer gz.Close()
		reader = gz
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	var runs []Run
	for scanner.Scan() {
		var r record
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue // a damaged line only loses that run
		}
		if (r.Purpose != "" && r.Purpose != "log_ranking") || legacyGroupRanking(r) || r.DryRun || r.Error != "" || len(r.Scores) == 0 {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, r.Time)
		if err != nil {
			continue
		}
		run := Run{
			Time:    when,
			Symptom: r.Request.State.Symptom,
			Window:  r.Request.State.WindowStart + " - " + r.Request.State.WindowEnd,
			Model:   r.Model,
			Logs:    r.Request.State.Logs,
		}
		sort.SliceStable(r.Scores, func(i, j int) bool { return r.Scores[i].Score > r.Scores[j].Score })
		for _, score := range r.Scores {
			run.Rankings = append(run.Rankings, Ranking{Path: score.Log, Priority: int(score.Score/3*100 + 0.5)})
		}
		runs = append(runs, run)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	return runs, nil
}

// Case is what the operator tells about a run.
type Case struct {
	Run           Run
	Causes        []string // paths; empty means the cause was in none of them
	Note          string
	OS            string
	JevtriVersion string
	Hostnames     []string // names of this host to replace
}

// Pseudonymizer replaces addresses and host names with stable labels, so
// that "the same address failed 40 times" still reads from the report.
type Pseudonymizer struct {
	hosts  []string
	users  []string
	labels map[string]string
	counts map[string]int
}

var (
	ipv4 = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)
	// Full form, or compressed with "::". Times of day (03:02:30) and MAC
	// addresses have too few groups for the full form and no "::"; C++ names
	// such as std::string are dropped by the neighbour check in Apply.
	ipv6 = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})?::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6})?`)
)

// NewPseudonymizer returns one that replaces the given host names and user
// names as well as addresses.
func NewPseudonymizer(hostnames []string, users ...string) *Pseudonymizer {
	return &Pseudonymizer{hosts: longestFirst(hostnames), users: longestFirst(users), labels: map[string]string{}, counts: map[string]int{}}
}

// longestFirst drops one-letter names, which would hit every word, and sorts
// the rest so that web01.example.com is not cut to [host-1].example.com.
func longestFirst(names []string) []string {
	var kept []string
	for _, name := range names {
		if len(name) >= 2 {
			kept = append(kept, name)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	return kept
}

// LocalUsers returns the accounts of people in an /etc/passwd file: UID 1000
// and above, except nobody. System accounts (root, www-data, mysql) tell
// which service failed and are kept.
func LocalUsers(passwd string) []string {
	var users []string
	for _, line := range strings.Split(passwd, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err == nil && uid >= 1000 && uid != 65534 {
			users = append(users, fields[0])
		}
	}
	return users
}

func (p *Pseudonymizer) label(kind, value string) string {
	key := kind + "\x00" + value
	if label, ok := p.labels[key]; ok {
		return label
	}
	p.counts[kind]++
	label := fmt.Sprintf("[%s-%d]", kind, p.counts[kind])
	p.labels[key] = label
	return label
}

// Apply replaces host names, then IPv4 and IPv6 addresses.
func (p *Pseudonymizer) Apply(text string) string {
	for _, host := range p.hosts {
		text = p.replaceName(text, "host", host)
	}
	for _, user := range p.users {
		text = p.replaceName(text, "user", user)
	}
	// IPv4 first: in ::ffff:192.0.2.1 the IPv6 pattern would stop at "192"
	// and leave ".0.2.1" readable.
	text = ipv4.ReplaceAllStringFunc(text, func(match string) string { return p.label("ip", match) })
	return replaceIPv6(text, p)
}

// replaceName replaces name where it stands as a whole word, ignoring case.
func (p *Pseudonymizer) replaceName(text, kind, name string) string {
	pattern := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9.-])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9-])`)
	if !pattern.MatchString(text) {
		return text
	}
	label := p.label(kind, strings.ToLower(name))
	// Twice: adjacent matches such as "a a" share the separator.
	for i := 0; i < 2; i++ {
		text = pattern.ReplaceAllString(text, "${1}"+label+"${2}")
	}
	return text
}

func replaceIPv6(text string, p *Pseudonymizer) string {
	var out strings.Builder
	last := 0
	for _, loc := range ipv6.FindAllStringIndex(text, -1) {
		match := text[loc[0]:loc[1]]
		if match == "::" || wordByte(text, loc[0]-1) || wordByte(text, loc[1]) {
			continue
		}
		out.WriteString(text[last:loc[0]])
		out.WriteString(p.label("ip", strings.ToLower(match)))
		last = loc[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// wordByte reports whether text[i] continues a word, so the match is part
// of a longer token rather than an address.
func wordByte(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return false
	}
	c := text[i]
	return c == '_' || c == ':' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// Counts returns how many distinct values of each kind were replaced.
func (p *Pseudonymizer) Counts() map[string]int { return p.counts }

// Markdown renders the report body.
func Markdown(c Case, p *Pseudonymizer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Real cause\n\n")
	if len(c.Causes) == 0 {
		b.WriteString("None of the ranked logs\n\n")
	} else {
		for _, cause := range c.Causes {
			fmt.Fprintf(&b, "- %s\n", code(p.Apply(cause)))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "### Note\n\n%s\n\n", quoted(p.Apply(c.Note)))
	fmt.Fprintf(&b, "### Symptom given with -i\n\n%s\n\n", quoted(p.Apply(c.Run.Symptom)))
	fmt.Fprintf(&b, "### Environment\n\n- OS: %s\n- jevtri: %s\n- Jev model: %s\n- Window: %s\n\n", code(c.OS), code(c.JevtriVersion), code(c.Run.Model), code(c.Run.Window))
	b.WriteString("### Ranking by jevtri\n\n| # | Log | Priority |\n|---|---|---|\n")
	for i, ranking := range c.Run.Rankings {
		fmt.Fprintf(&b, "| %d | %s | %d |\n", i+1, code(p.Apply(ranking.Path)), ranking.Priority)
	}
	b.WriteString("\n### Lines sent to Jev (masked)\n\n")
	for _, ranking := range c.Run.Rankings {
		fmt.Fprintf(&b, "<details><summary>%s</summary>\n\n%s\n\n</details>\n\n", code(p.Apply(ranking.Path)), fence(p.Apply(c.Run.Logs[ranking.Path])))
	}
	return b.String()
}

// code shows text from logs or the host literally, in Markdown text, a table
// cell or HTML alike: a path such as a`b|c</details> must not end the code
// span, split the table row or close the folded section.
func code(s string) string {
	if s == "" {
		return "<em>none</em>"
	}
	escaped := strings.NewReplacer("|", "&#124;", "`", "&#96;", "\n", " ", "\r", " ").Replace(html.EscapeString(s))
	return "<code>" + escaped + "</code>"
}

// quoted shows free text (symptom, note) literally.
func quoted(s string) string {
	if strings.TrimSpace(s) == "" {
		return "<em>none</em>"
	}
	return fence(s)
}

// fence wraps text in a code block whose fence is longer than any run of
// backticks inside, so log text cannot close it.
func fence(text string) string {
	longest := 0
	for _, run := range regexp.MustCompile("`+").FindAllString(text, -1) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	marks := strings.Repeat("`", max(3, longest+1))
	return marks + "text\n" + text + "\n" + marks
}

// FormURL opens the issue form with the short fields filled in. The body is
// too long for a URL and is pasted or attached from the file. The title holds
// paths, so it gets the same replacements as the body.
func FormURL(c Case, p *Pseudonymizer) string {
	values := url.Values{}
	values.Set("template", IssueTemplate)
	title := "Case: cause not in the ranked logs"
	if len(c.Causes) > 0 {
		title = p.Apply("Case: cause in " + strings.Join(c.Causes, ", "))
	}
	values.Set("title", title)
	values.Set("jevtri-version", c.JevtriVersion)
	values.Set("os", c.OS)
	return IssueURL + "?" + values.Encode()
}

// OSName returns PRETTY_NAME from an os-release file, or "".
func OSName(osRelease string) string {
	for _, line := range strings.Split(osRelease, "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"'`)
		}
	}
	return ""
}

// Old first-stage records had no purpose, but used a distinct task prompt.
func legacyGroupRanking(r record) bool {
	return r.Purpose == "" && strings.Contains(r.Request.State.Task, "Decide which group an engineer should examine in detail first to find the cause.")
}
