package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

var jst = time.FixedZone("JST", 9*3600)

func sample() Report {
	reference := time.Date(2026, 9, 28, 15, 47, 0, 0, jst)
	return Report{
		Reference: reference, WindowStart: reference.Add(-5 * time.Minute), WindowEnd: reference.Add(5 * time.Minute), Minutes: 5,
		Symptom: "The website cannot be reached",
		Scores: []jev.Score{
			{Path: "/var/log/syslog", Raw: 2.6, Priority: 2.6 / 3, Confidence: 0.8},
			{Path: "/var/log/nginx/error.log", Raw: 2.97, Priority: 0.99, Confidence: 0.95},
		},
		Logs: []LogInfo{
			{Name: "system", Path: "/var/log/syslog", TimeFormat: "rfc3339", Entries: 40, SentEntries: 40, SentBytes: 5000, Masked: map[string]int{"email": 2}},
			{Name: "nginx", Path: "/var/log/nginx/error.log", TimeFormat: "slash-ymd", Entries: 3, SentEntries: 3, SentBytes: 400},
			{Name: "db", Path: "/var/log/mysql/error.log", Skipped: "permission denied"},
		},
		Model: "jev-1.13.0", Latency: 240 * time.Millisecond, InputTokens: 2000, SentLog: "/var/log/jevtri/sent.log",
	}
}

// OUT-01 and EX-01: ranking, no percent sign, first log, unevaluated logs.
func TestText(t *testing.T) {
	var out bytes.Buffer
	Text(&out, sample(), false)
	text := out.String()
	for _, want := range []string{
		"Symptom:        The website cannot be reached",
		"1. /var/log/nginx/error.log   99",
		"2. /var/log/syslog            87",
		"Start with: /var/log/nginx/error.log",
		"Not evaluated:\n  - /var/log/mysql/error.log: permission denied",
		"not the probability of being the cause",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "%") {
		t.Error("priorities must not be shown with %")
	}
	if strings.Contains(text, "3. /var/log/mysql") {
		t.Error("an unevaluated log must not be ranked")
	}
}

// OUT-04: all priorities low.
func TestNoClearLead(t *testing.T) {
	r := sample()
	r.Scores = []jev.Score{{Path: "/var/log/syslog", Priority: 0.2}, {Path: "/var/log/nginx/error.log", Priority: 0.1}}
	var out bytes.Buffer
	Text(&out, r, false)
	if !strings.Contains(out.String(), "No log shows a clear lead") || strings.Contains(out.String(), "Start with") {
		t.Errorf("unexpected:\n%s", out.String())
	}
}

// OUT-03: verbose details without log content.
func TestVerbose(t *testing.T) {
	var out bytes.Buffer
	Text(&out, sample(), true)
	text := out.String()
	for _, want := range []string{"time_format=rfc3339 entries=40 sent=40 (5000 bytes)", "masked: email=2", "model=jev-1.13.0 latency=240ms", "recorded in: /var/log/jevtri/sent.log", "not evaluated: permission denied"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// OUT-02: JSON with priority 0..1 in descending order.
func TestJSON(t *testing.T) {
	var out bytes.Buffer
	if err := JSON(&out, sample()); err != nil {
		t.Fatal(err)
	}
	var decoded jsonReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.IncidentTime != "2026-09-28T15:47:00+09:00" || decoded.WindowMinutes != 5 || len(decoded.Results) != 2 {
		t.Fatalf("unexpected %+v", decoded)
	}
	if decoded.Results[0].Log != "/var/log/nginx/error.log" || decoded.Results[0].Priority != 0.99 || decoded.Results[1].Priority != 0.867 {
		t.Errorf("unexpected results %+v", decoded.Results)
	}
	if len(decoded.NotEvaluated) != 1 || decoded.NotEvaluated[0].Reason != "permission denied" {
		t.Errorf("unexpected not_evaluated %+v", decoded.NotEvaluated)
	}
}

// RX-22 (R-22): a log that kept nothing because reading stopped early is
// not shown as a log with nothing in the window.
func TestZeroEntriesAfterReadTruncation(t *testing.T) {
	r := sample()
	r.Logs = append(r.Logs, LogInfo{Name: "trace", Path: "/var/log/trace.log", TimeFormat: "iso-space", ReadTruncated: true, BytesRead: 4 << 20})
	var out bytes.Buffer
	Text(&out, r, true)
	text := out.String()
	for _, want := range []string{
		"  - /var/log/trace.log (reading stopped early; entries may have been left out)",
		"no entries kept; not sent",
		"read truncated:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "iso-space no entries in the window") {
		t.Error("the truncated log is shown as having nothing in the window")
	}
}

// RX-31 (R-31): when every log kept nothing and reading stopped early in one,
// the summary does not claim the window was empty.
func TestSummaryAfterReadTruncation(t *testing.T) {
	r := sample()
	r.Scores = nil
	r.Logs = []LogInfo{{Name: "trace", Path: "/var/log/trace.log", TimeFormat: "iso-space", ReadTruncated: true}}
	var out bytes.Buffer
	Text(&out, r, false)
	text := out.String()
	if strings.Contains(text, "No configured log has entries in the window") {
		t.Errorf("the summary claims an empty window:\n%s", text)
	}
	if !strings.Contains(text, "Reading stopped early") {
		t.Errorf("the summary does not say reading stopped early:\n%s", text)
	}
	r.Logs[0].ReadTruncated = false
	out.Reset()
	Text(&out, r, false)
	if !strings.Contains(out.String(), "No configured log has entries in the window") {
		t.Errorf("an empty window is no longer reported:\n%s", out.String())
	}
}
