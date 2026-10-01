package logread

import (
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/timefmt"
	"github.com/takeshiue/jevtri/internal/window"
)

var tokyo = time.FixedZone("JST", 9*3600)

func mustFormat(t *testing.T, name string) *timefmt.Format {
	t.Helper()
	format, err := timefmt.Resolve(name)
	if err != nil {
		t.Fatal(err)
	}
	return format
}

func mustWindow(t *testing.T, reference string, minutes int) window.Window {
	t.Helper()
	now, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-28 23:00:00", tokyo)
	w, err := window.Parse(reference, minutes, now)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func write(t *testing.T, path, content string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func writeGzip(t *testing.T, path, content string, modified time.Time) {
	t.Helper()
	handle, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(handle)
	gz.Write([]byte(content))
	gz.Close()
	handle.Close()
	os.Chtimes(path, modified, modified)
}

func texts(entries []Entry) string {
	var lines []string
	for _, entry := range entries {
		lines = append(lines, entry.Text)
	}
	return strings.Join(lines, "|")
}

// LR-01: lines without a timestamp stay with the preceding event, using the
// real Tomcat sample with a stack trace.
func TestContinuationLines(t *testing.T) {
	src := Source{
		Name: "tomcat", Path: filepath.Join("..", "..", "testdata", "logs", "tomcat", "catalina.2026-09-28.log"),
		Format: mustFormat(t, "dmy-month"), Location: tokyo,
	}
	result, err := Read(src, mustWindow(t, "2026-09-28 09:44:02", 1), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	var severe *Entry
	for i := range result.Entries {
		if strings.Contains(result.Entries[i].Text, "Parse fatal error") {
			severe = &result.Entries[i]
			break
		}
	}
	if severe == nil {
		t.Fatalf("SEVERE entry not found among %d entries", len(result.Entries))
	}
	if !strings.Contains(severe.Text, "\n\torg.xml.sax.SAXParseException") {
		t.Errorf("stack trace not attached:\n%s", severe.Text)
	}
	if !strings.HasPrefix(severe.Text, "2026-09-28T09:44:02.397+09:00 SEVERE") {
		t.Errorf("timestamp not rewritten: %q", severe.Text[:60])
	}
}

// LR-02 and LR-03: rotated files in both naming styles, with .gz only when
// allowed.
func TestRotatedFiles(t *testing.T) {
	now, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-28 23:00:00", tokyo)
	for _, style := range []struct{ name, plain, gz string }{
		{"numbered", ".1", ".2.gz"},
		{"dated", "-20260928", "-20260927.gz"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "messages")
		write(t, path, "Sep 28 10:01:00 web01 app: current-1\nSep 28 10:02:00 web01 app: current-2\n", now)
		write(t, path+style.plain, "Sep 28 09:58:00 web01 app: rotated-1\nSep 28 09:59:30 web01 app: rotated-2\n", now.Add(-time.Hour))
		writeGzip(t, path+style.gz, "Sep 28 09:56:00 web01 app: compressed-1\n", now.Add(-2*time.Hour))
		// Unrelated files that must be ignored.
		write(t, path+".bak", "Sep 28 09:59:00 web01 app: backup\n", now)
		write(t, path+"-old.txt", "Sep 28 09:59:00 web01 app: other\n", now)

		w := mustWindow(t, "2026-09-28 10:00:00", 5)
		for _, allowCompressed := range []bool{false, true} {
			src := Source{Name: "system", Path: path, Format: mustFormat(t, "syslog"), Location: tokyo, ReadCompressed: allowCompressed}
			result, err := Read(src, w, tokyo)
			if err != nil {
				t.Fatal(err)
			}
			got := texts(result.Entries)
			want := "rotated-1|rotated-2|current-1|current-2"
			if allowCompressed {
				want = "compressed-1|" + want
			}
			var names []string
			for _, part := range strings.Split(got, "|") {
				names = append(names, part[strings.LastIndex(part, " ")+1:])
			}
			if strings.Join(names, "|") != want {
				t.Errorf("%s compressed=%v: got %s, want %s", style.name, allowCompressed, strings.Join(names, "|"), want)
			}
		}
	}
}

func TestUnreadableCurrentFile(t *testing.T) {
	src := Source{Name: "missing", Path: filepath.Join(t.TempDir(), "nope.log"), Format: mustFormat(t, "syslog"), Location: tokyo}
	if _, err := Read(src, mustWindow(t, "2026-09-28 10:00:00", 5), tokyo); err == nil {
		t.Fatal("expected an error")
	}
}

// EX-04: a log whose lines carry no timestamp is refused, not sent empty.
func TestNoTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boot.log")
	os.WriteFile(path, []byte("[  OK  ] Started Show Plymouth Boot Screen.\n[FAILED] Failed to start nginx.service.\n"), 0o644)
	src := Source{Name: "boot", Path: path, Format: mustFormat(t, "syslog"), Location: tokyo}
	_, err := Read(src, mustWindow(t, "2026-09-28 10:00:00", 5), tokyo)
	if !errors.Is(err, ErrNoTimestamps) {
		t.Fatalf("got %v, want ErrNoTimestamps", err)
	}
}

// An empty or blank log (just rotated, for example) has nothing in the
// window but is not an error.
func TestBlankLogIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages")
	os.WriteFile(path, []byte("\n\n"), 0o644)
	src := Source{Name: "system", Path: path, Format: mustFormat(t, "syslog"), Location: tokyo}
	result, err := Read(src, mustWindow(t, "2026-09-28 10:00:00", 5), tokyo)
	if err != nil || len(result.Entries) != 0 {
		t.Fatalf("got %v, %d entries", err, len(result.Entries))
	}
}

// LR-05: a path with a glob reads the matching files that can hold the
// window, merged by time, and skips files last written before it.
func TestPatternPath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, modified time.Time) {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(content), 0o644)
		os.Chtimes(path, modified, modified)
	}
	at := func(clock string) time.Time {
		value, _ := time.ParseInLocation("2006-01-02 15:04:05", clock, tokyo)
		return value
	}
	write("catalina.2026-09-27.log", "27-Sep-2026 10:00:00.000 INFO old day\n", at("2026-09-27 23:59:00"))
	write("catalina.2026-09-28.log", "28-Sep-2026 10:00:10.000 SEVERE second\n", at("2026-09-28 10:00:10"))
	write("side.2026-09-28.log", "28-Sep-2026 10:00:05.000 INFO first\n", at("2026-09-28 10:00:05"))
	src := Source{Name: "tomcat", Path: filepath.Join(dir, "*.2026-09-2?.log"), Format: mustFormat(t, "dmy-month"), Location: tokyo}
	result, err := Read(src, mustWindow(t, "2026-09-28 10:00:00", 5), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || len(result.Entries) != 2 {
		t.Fatalf("files %v entries %d", result.Files, len(result.Entries))
	}
	if !strings.Contains(result.Entries[0].Text, "first") || !strings.Contains(result.Entries[1].Text, "second") {
		t.Errorf("entries not merged by time: %+v", result.Entries)
	}
}

func TestPatternWithoutMatches(t *testing.T) {
	src := Source{Name: "tomcat", Path: filepath.Join(t.TempDir(), "catalina.*.log"), Format: mustFormat(t, "dmy-month"), Location: tokyo}
	if _, err := Read(src, mustWindow(t, "2026-09-28 10:00:00", 5), tokyo); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("got %v", err)
	}
}

// LR-04: a large file is not read from the beginning.
func TestLargeFileSeeks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	var builder strings.Builder
	start := time.Date(2026, 9, 27, 0, 0, 0, 0, tokyo)
	padding := strings.Repeat("x", 180)
	for i := 0; i < 86400; i++ { // one line per second for a day, about 17 MB
		fmt.Fprintf(&builder, "%s app line %d %s\n", start.Add(time.Duration(i)*time.Second).Format("2006-01-02T15:04:05-07:00"), i, padding)
	}
	write(t, path, builder.String(), time.Now())
	size := int64(builder.Len())

	src := Source{Name: "big", Path: path, Format: mustFormat(t, "rfc3339"), Location: tokyo}
	result, err := Read(src, mustWindow(t, "2026-09-27 23:50:00", 5), tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 601 { // 23:45:00 .. 23:55:00 inclusive
		t.Errorf("entries = %d, want 601", len(result.Entries))
	}
	if result.BytesRead > size/5 {
		t.Errorf("read %d of %d bytes; expected to skip most of the file", result.BytesRead, size)
	}
}

// SEC-008: an event followed by a huge number of lines without a timestamp
// (a runaway stack trace, or a gzip bomb of them) must be read in linear time
// and kept within maxEntryBytes.
func TestLongContinuationIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	var content strings.Builder
	content.WriteString("2026-09-28 03:02:00 ERROR start of a huge event\n")
	filler := strings.Repeat("x", 199)
	for i := 0; i < 20000; i++ {
		content.WriteString(filler + "\n")
	}
	content.WriteString("2026-09-28 03:02:30 INFO next event\n")
	write(t, path, content.String(), time.Date(2026, 9, 28, 3, 3, 0, 0, tokyo))
	src := Source{Name: "app", Path: path, Format: mustFormat(t, "iso-space"), Location: tokyo}
	started := time.Now()
	result, err := Read(src, mustWindow(t, "2026-09-28 03:02:00", 5), tokyo)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("%d entries, want 2", len(result.Entries))
	}
	if n := len(result.Entries[0].Text); n > maxEntryBytes+200 {
		t.Errorf("first entry is %d bytes, over the %d cap", n, maxEntryBytes)
	}
	if !strings.Contains(result.Entries[0].Text, "more lines not kept") {
		t.Errorf("the cut is not marked: ...%s", result.Entries[0].Text[len(result.Entries[0].Text)-80:])
	}
	if !strings.HasSuffix(result.Entries[1].Text, "INFO next event") {
		t.Errorf("the next event is lost: %q", result.Entries[1].Text)
	}
	// 4 MB of lines takes about a minute when every line copies the event so far.
	if elapsed > 10*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

// SEC-004: a line longer than the buffer must not make the whole log
// unreadable; the lines before it are still used.
func TestOverlongLineKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	content := "2026-09-28 03:02:00 ERROR disk full\n" +
		"2026-09-28 03:02:10 INFO " + strings.Repeat("y", maxLineBytes) + "\n" +
		"2026-09-28 03:02:20 INFO after the long line\n"
	write(t, path, content, time.Date(2026, 9, 28, 3, 3, 0, 0, tokyo))
	src := Source{Name: "app", Path: path, Format: mustFormat(t, "iso-space"), Location: tokyo}
	result, err := Read(src, mustWindow(t, "2026-09-28 03:02:00", 5), tokyo)
	if err != nil {
		t.Fatalf("the whole log was lost: %v", err)
	}
	if len(result.Entries) != 1 || !strings.HasSuffix(result.Entries[0].Text, "ERROR disk full") {
		t.Errorf("entries: %q", texts(result.Entries))
	}
	if !result.Truncated {
		t.Error("Truncated is not set although the file was cut short")
	}
}

// A window full of events must not grow the kept text without a limit
// (SEC-008): reading trims while it reads, not only after the whole file.
func TestReadTrimsWhileReading(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	var b strings.Builder
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	const lines = 2000
	for i := 0; i < lines; i++ {
		b.WriteString(base.Format("Jan  2 15:04:05") + " host app: line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "big", Path: path, Format: mustFormat(t, "syslog"), Location: time.UTC}
	w := window.Window{Reference: base, Start: base.Add(-time.Hour), End: base.Add(time.Hour)}

	saved := maxKeptBytes
	maxKeptBytes = 4 << 10
	defer func() { maxKeptBytes = saved }()
	result, err := Read(src, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) >= lines {
		t.Fatalf("kept %d entries of %d: the byte limit did not trim while reading", len(result.Entries), lines)
	}
	if result.Entries[len(result.Entries)-1].Time != base {
		t.Error("the newest entry was not kept")
	}
}

// R-02: continuation lines count toward maxKeptBytes. Each event here is a
// short first line followed by a long continuation, so counting only first
// lines would keep every event.
func TestContinuationCountsTowardKeptLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	const events = 256
	continuation := strings.Repeat("x", 1000)
	var b strings.Builder
	for i := 0; i < events; i++ {
		b.WriteString(base.Format(time.RFC3339) + " event\n" + continuation + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "trace", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC}
	w := window.Window{Reference: base, Start: base.Add(-time.Minute), End: base.Add(time.Minute)}

	saved := maxKeptBytes
	maxKeptBytes = 32 << 10
	defer func() { maxKeptBytes = saved }()
	result, err := Read(src, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, entry := range result.Entries {
		total += len(entry.Text)
	}
	if total > maxKeptBytes {
		t.Errorf("kept %d bytes of text, over the %d limit", total, maxKeptBytes)
	}
	if !result.Truncated {
		t.Error("Truncated is not set although events were dropped")
	}
	if !strings.HasSuffix(result.Entries[len(result.Entries)-1].Text, continuation) {
		t.Error("the newest event lost its continuation")
	}
}

// AD-11: maxKeptBytes holds across the files of one log, rotated siblings
// and files matched by a pattern alike.
func TestKeptLimitAcrossFiles(t *testing.T) {
	saved := maxKeptBytes
	maxKeptBytes = 16 << 10
	defer func() { maxKeptBytes = saved }()
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(base.Add(time.Duration(i)*time.Second).Format(time.RFC3339) + " event\n" + strings.Repeat("x", 1000) + "\n")
	}
	body := b.String() // about 10 KiB, under the limit alone
	rotated := t.TempDir()
	current := filepath.Join(rotated, "app.log")
	write(t, current, body, base)
	pattern := t.TempDir()
	write(t, filepath.Join(pattern, "svc0.log"), body, base)
	for i := 1; i <= 5; i++ {
		write(t, fmt.Sprintf("%s.%d", current, i), body, base.Add(-time.Duration(i)*time.Second))
		write(t, filepath.Join(pattern, fmt.Sprintf("svc%d.log", i)), body, base)
	}
	w := window.Window{Reference: base, Start: base.Add(-time.Hour), End: base.Add(time.Hour)}
	for _, path := range []string{current, filepath.Join(pattern, "svc*.log")} {
		result, err := Read(Source{Name: "many", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC}, w, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		if kept := textBytes(result.Entries); kept > maxKeptBytes {
			t.Errorf("%s: kept %d bytes over the %d limit", path, kept, maxKeptBytes)
		}
		if !result.Truncated {
			t.Errorf("%s: Truncated is not set", path)
		}
		if len(result.Entries) == 0 || !result.Entries[len(result.Entries)-1].Time.Equal(base.Add(9*time.Second)) {
			t.Errorf("%s: the newest entry was not kept", path)
		}
	}
}

// RW-11 (R-11): when the current file alone fills the limit, older rotated
// files add nothing, and only one entry in the whole log may exceed it.
func TestKeptLimitWhenCurrentFileFills(t *testing.T) {
	saved := maxKeptBytes
	maxKeptBytes = 100
	defer func() { maxKeptBytes = saved }()
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	line := base.Format(time.RFC3339) + " " + strings.Repeat("x", 60) + "\n"
	dir := t.TempDir()
	current := filepath.Join(dir, "app.log")
	write(t, current, line, base)
	write(t, current+".1", strings.Replace(line, "x", "o", 1), base.Add(-time.Second))
	w := window.Window{Reference: base, Start: base.Add(-time.Minute), End: base.Add(time.Minute)}
	result, err := Read(Source{Name: "app", Path: current, Format: mustFormat(t, "rfc3339"), Location: time.UTC}, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if kept := textBytes(result.Entries); kept > maxKeptBytes {
		t.Errorf("kept %d bytes over the %d limit", kept, maxKeptBytes)
	}
	if len(result.Entries) != 1 || strings.Contains(result.Entries[0].Text, "o") {
		t.Errorf("entries = %q, want only the current file's entry", result.Entries)
	}
	if !result.Truncated {
		t.Error("Truncated is not set")
	}
}

// RW-12 (R-12): a log written newest first keeps its newest events, judged
// by time and not by the order of the lines.
func TestKeepsNewestWhenReversed(t *testing.T) {
	saved := maxKeptBytes
	maxKeptBytes = 120
	defer func() { maxKeptBytes = saved }()
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 9; i >= 0; i-- {
		b.WriteString(base.Add(time.Duration(i)*time.Second).Format(time.RFC3339) + " " + strings.Repeat("x", 60) + "\n")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rev.log")
	write(t, path, b.String(), base)
	w := window.Window{Reference: base, Start: base.Add(-time.Minute), End: base.Add(time.Minute)}
	result, err := Read(Source{Name: "rev", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC}, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	newest := false
	for _, entry := range result.Entries {
		if entry.Time.Equal(base) {
			t.Error("the oldest event was kept")
		}
		newest = newest || entry.Time.Equal(base.Add(9*time.Second))
	}
	if !newest {
		t.Errorf("the newest event was not kept: %v", result.Entries)
	}
	if kept := textBytes(result.Entries); kept > maxKeptBytes {
		t.Errorf("kept %d bytes over the %d limit", kept, maxKeptBytes)
	}
}

// RW-13 (R-13): continuation lines left out of an event are reported.
func TestLongContinuationSetsTruncated(t *testing.T) {
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")
	write(t, path, base.Format(time.RFC3339)+" event\n"+strings.Repeat("x", 70000)+"\n", base)
	w := window.Window{Reference: base, Start: base.Add(-time.Minute), End: base.Add(time.Minute)}
	result, err := Read(Source{Name: "trace", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC}, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || !result.Truncated {
		t.Errorf("entries = %d, Truncated = %v; want 1 and true", len(result.Entries), result.Truncated)
	}
}

// RX-21 (R-21): across the files of one log the newest events by time are
// kept, whichever file holds them, and files after the limit is reached are
// still read.
func TestKeepsNewestAcrossRotatedFiles(t *testing.T) {
	saved := maxKeptBytes
	defer func() { maxKeptBytes = saved }()
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	line := func(offset time.Duration, fill string) string {
		return base.Add(offset).Format(time.RFC3339) + " " + strings.Repeat(fill, 60) + "\n"
	}
	w := window.Window{Reference: base, Start: base.Add(-time.Hour), End: base.Add(time.Hour)}
	read := func(t *testing.T, path string) Result {
		t.Helper()
		result, err := Read(Source{Name: "app", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC, ReadCompressed: true}, w, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	times := func(entries []Entry) []string {
		var out []string
		for _, entry := range entries {
			out = append(out, entry.Time.Format("15:04:05"))
		}
		return out
	}
	for _, rotated := range []string{".1", ".1.gz"} {
		t.Run("rotated newer"+rotated, func(t *testing.T) {
			maxKeptBytes = 100
			dir := t.TempDir()
			current := filepath.Join(dir, "app.log")
			write(t, current, line(0, "x"), base.Add(time.Minute))
			if strings.HasSuffix(rotated, ".gz") {
				writeGzip(t, current+rotated, line(9*time.Second, "y"), base)
			} else {
				write(t, current+rotated, line(9*time.Second, "y"), base)
			}
			result := read(t, current)
			if got := times(result.Entries); len(got) != 1 || got[0] != "12:00:09" {
				t.Errorf("kept %v, want [12:00:09]", got)
			}
			if kept := textBytes(result.Entries); kept > maxKeptBytes || !result.Truncated {
				t.Errorf("kept %d bytes (limit %d), Truncated %v", kept, maxKeptBytes, result.Truncated)
			}
		})
	}
	t.Run("newest after the limit", func(t *testing.T) {
		maxKeptBytes = 100
		dir := t.TempDir()
		current := filepath.Join(dir, "app.log")
		write(t, current, line(0, "x"), base.Add(time.Minute))
		write(t, current+".1", line(5*time.Second, "y"), base)
		write(t, current+".2", line(9*time.Second, "z"), base.Add(-time.Minute))
		if got := times(read(t, current).Entries); len(got) != 1 || got[0] != "12:00:09" {
			t.Errorf("kept %v, want [12:00:09]", got)
		}
	})
	t.Run("one entry over the limit per log", func(t *testing.T) {
		maxKeptBytes = 10
		dir := t.TempDir()
		current := filepath.Join(dir, "app.log")
		write(t, current, line(9*time.Second, "x"), base.Add(time.Minute))
		write(t, current+".1", line(5*time.Second, "y"), base)
		if got := times(read(t, current).Entries); len(got) != 1 || got[0] != "12:00:09" {
			t.Errorf("kept %v, want [12:00:09]", got)
		}
	})
	t.Run("order and ties", func(t *testing.T) {
		maxKeptBytes = 260
		dir := t.TempDir()
		current := filepath.Join(dir, "app.log")
		write(t, current, line(time.Second, "c")+line(2*time.Second, "d"), base.Add(time.Minute))
		write(t, current+".1", line(0, "a")+line(time.Second, "b"), base)
		result := read(t, current)
		var fills []string
		for _, entry := range result.Entries {
			fills = append(fills, entry.Text[len(entry.Text)-1:])
		}
		// 12:00:00 "a" goes; the tie at 12:00:01 keeps file order (b, then c).
		if got := strings.Join(fills, ""); got != "bcd" {
			t.Errorf("kept %q, want \"bcd\"", got)
		}
	})
}
