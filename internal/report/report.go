// Package report prints the ranking for people (text, -v) and machines
// (--json).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// LowThreshold is the priority below which every log is considered to show
// no clear lead (spec: tell the user to look beyond the configured logs).
const LowThreshold = 0.34

// LogInfo describes how one configured log was handled, for -v and for
// logs that could not be evaluated.
type LogInfo struct {
	Name        string
	Path        string
	TimeFormat  string
	Entries     int
	SentEntries int
	SentBytes   int
	Dropped     int
	Masked      map[string]int
	Files       []string
	BytesRead   int64
	// ReadTruncated is set when reading left out part of the window (limits
	// on entries, bytes or line length). Dropped counts only what the send
	// budget left out afterwards.
	ReadTruncated bool
	// Skipped is set when the log could not be evaluated; it then has no rank.
	Skipped string
}

// Quiet reports whether the log was read but had nothing in the window.
// Such a log is not sent and has no rank.
func (info LogInfo) Quiet() bool { return info.Skipped == "" && info.Entries == 0 }

// Report is everything printed.
type Report struct {
	Reference   time.Time
	WindowStart time.Time
	WindowEnd   time.Time
	Minutes     int
	Symptom     string
	Scores      []jev.Score // in configuration order
	Logs        []LogInfo   // in configuration order
	Model       string
	Latency     time.Duration
	InputTokens int
	SentLog     string
	DryRun      bool
}

// Text writes the human-readable report.
func Text(w io.Writer, r Report, verbose bool) {
	layout := "2006-01-02 15:04:05 MST"
	fmt.Fprintf(w, "Reference time: %s\n", r.Reference.Format(layout))
	fmt.Fprintf(w, "Window:         %s .. %s (%d min)\n", r.WindowStart.Format("15:04:05"), r.WindowEnd.Format("15:04:05"), r.Minutes)
	if r.Symptom != "" {
		fmt.Fprintf(w, "Symptom:        %s\n", r.Symptom)
	}
	fmt.Fprintln(w)

	if len(r.Scores) == 0 && !r.DryRun && anyReadTruncated(r.Logs) {
		// Reading stopped early in some log, so an empty window is not known.
		fmt.Fprintln(w, "No entries could be kept to send; nothing was sent to Jev.")
		fmt.Fprintln(w, "Reading stopped early (size limits), so entries in the window may have been left out.")
	} else if len(r.Scores) == 0 && !r.DryRun {
		fmt.Fprintln(w, "No configured log has entries in the window; nothing was sent to Jev.")
		fmt.Fprintln(w, "Widen the window with -m or check the time given with -t.")
	} else if r.DryRun {
		fmt.Fprintln(w, "Dry run: nothing was sent to Jev.")
	} else {
		ranked := jev.Ranked(r.Scores)
		width := 0
		for _, score := range ranked {
			width = max(width, len(score.Path))
		}
		fmt.Fprintln(w, "Investigation priority (0-100; not the probability of being the cause):")
		for i, score := range ranked {
			fmt.Fprintf(w, "  %2d. %-*s  %3d\n", i+1, width, score.Path, percent(score.Priority))
		}
		if len(ranked) > 0 {
			fmt.Fprintln(w)
			if ranked[0].Priority < LowThreshold {
				fmt.Fprintln(w, "No log shows a clear lead. The cause may be in a log that is not configured,")
				fmt.Fprintln(w, "in the application itself, or outside this host.")
			} else {
				fmt.Fprintf(w, "Start with: %s\n", ranked[0].Path)
			}
		}
	}

	var skipped []LogInfo
	for _, info := range r.Logs {
		if info.Skipped != "" {
			skipped = append(skipped, info)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Not evaluated:")
		for _, info := range skipped {
			fmt.Fprintf(w, "  - %s: %s\n", info.Path, info.Skipped)
		}
	}
	if quiet := quietPaths(r.Logs); len(quiet) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "No entries in the window (not sent):")
		for _, info := range r.Logs {
			if !info.Quiet() {
				continue
			}
			if info.ReadTruncated {
				fmt.Fprintf(w, "  - %s (reading stopped early; entries may have been left out)\n", info.Path)
			} else {
				fmt.Fprintf(w, "  - %s\n", info.Path)
			}
		}
	}

	if verbose {
		verboseText(w, r)
	}
}

func verboseText(w io.Writer, r Report) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Details:")
	fmt.Fprintf(w, "  window: %s .. %s\n", r.WindowStart.Format(time.RFC3339), r.WindowEnd.Format(time.RFC3339))
	for _, info := range r.Logs {
		fmt.Fprintf(w, "  [%s] %s\n", info.Name, info.Path)
		if info.Skipped != "" {
			fmt.Fprintf(w, "      not evaluated: %s\n", info.Skipped)
			continue
		}
		if info.Quiet() {
			if info.ReadTruncated {
				// Reading stopped early, so "nothing in the window" is not known.
				fmt.Fprintf(w, "      time_format=%s no entries kept; not sent (read=%d bytes)\n", info.TimeFormat, info.BytesRead)
				fmt.Fprintln(w, "      read truncated: part of the window was left out while reading (size limits)")
				continue
			}
			fmt.Fprintf(w, "      time_format=%s no entries in the window; not sent (read=%d bytes)\n", info.TimeFormat, info.BytesRead)
			continue
		}
		fmt.Fprintf(w, "      time_format=%s entries=%d sent=%d (%d bytes) dropped=%d read=%d bytes\n",
			info.TimeFormat, info.Entries, info.SentEntries, info.SentBytes, info.Dropped, info.BytesRead)
		if info.ReadTruncated {
			fmt.Fprintln(w, "      read truncated: part of the window was left out while reading (size limits)")
		}
		if len(info.Files) > 1 {
			fmt.Fprintf(w, "      files: %s\n", strings.Join(info.Files, ", "))
		}
		if len(info.Masked) > 0 {
			fmt.Fprintf(w, "      masked: %s\n", formatCounts(info.Masked))
		}
	}
	if !r.DryRun && len(r.Scores) > 0 {
		fmt.Fprintf(w, "  jev: model=%s latency=%dms input_tokens=%d\n", r.Model, r.Latency.Milliseconds(), r.InputTokens)
		for _, score := range r.Scores {
			fmt.Fprintf(w, "      %s score=%.2f/3 confidence=%.2f\n", score.Path, score.Raw, score.Confidence)
		}
	}
	if r.SentLog != "" {
		fmt.Fprintf(w, "  recorded in: %s\n", r.SentLog)
	}
	fmt.Fprintln(w, "  Priorities rank where to look first; they are not probabilities of the cause.")
}

func anyReadTruncated(logs []LogInfo) bool {
	for _, info := range logs {
		if info.Quiet() && info.ReadTruncated {
			return true
		}
	}
	return false
}

func quietPaths(logs []LogInfo) []string {
	var paths []string
	for _, info := range logs {
		if info.Quiet() {
			paths = append(paths, info.Path)
		}
	}
	return paths
}

func formatCounts(counts map[string]int) string {
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, counts[kind]))
	}
	return strings.Join(parts, " ")
}

func percent(priority float64) int { return int(priority*100 + 0.5) }

type jsonReport struct {
	IncidentTime  string       `json:"incident_time"`
	WindowStart   string       `json:"window_start"`
	WindowEnd     string       `json:"window_end"`
	WindowMinutes int          `json:"window_minutes"`
	Symptom       string       `json:"symptom,omitempty"`
	DryRun        bool         `json:"dry_run,omitempty"`
	Model         string       `json:"model,omitempty"`
	Results       []jsonResult `json:"results"`
	NotEvaluated  []jsonSkip   `json:"not_evaluated,omitempty"`
	NoEntries     []string     `json:"no_entries,omitempty"`
	ReadTruncated []string     `json:"read_truncated,omitempty"`
}

type jsonResult struct {
	Log      string  `json:"log"`
	Priority float64 `json:"priority"`
}

type jsonSkip struct {
	Log    string `json:"log"`
	Reason string `json:"reason"`
}

// JSON writes the machine-readable report. priority is 0..1 as in the RFP.
func JSON(w io.Writer, r Report) error {
	out := jsonReport{
		IncidentTime:  r.Reference.Format(time.RFC3339),
		WindowStart:   r.WindowStart.Format(time.RFC3339),
		WindowEnd:     r.WindowEnd.Format(time.RFC3339),
		WindowMinutes: r.Minutes,
		Symptom:       r.Symptom,
		DryRun:        r.DryRun,
		Model:         r.Model,
		Results:       []jsonResult{},
	}
	for _, score := range jev.Ranked(r.Scores) {
		out.Results = append(out.Results, jsonResult{Log: score.Path, Priority: round(score.Priority)})
	}
	for _, info := range r.Logs {
		if info.Skipped != "" {
			out.NotEvaluated = append(out.NotEvaluated, jsonSkip{Log: info.Path, Reason: info.Skipped})
		}
	}
	out.NoEntries = quietPaths(r.Logs)
	for _, info := range r.Logs {
		if info.ReadTruncated {
			out.ReadTruncated = append(out.ReadTruncated, info.Path)
		}
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func round(value float64) float64 { return float64(int(value*1000+0.5)) / 1000 }
