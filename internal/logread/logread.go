// Package logread extracts the entries of one configured log that fall in
// the examined window, including rotated and optionally compressed files.
package logread

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/takeshiue/jevtri/internal/safeopen"
	"github.com/takeshiue/jevtri/internal/timefmt"
	"github.com/takeshiue/jevtri/internal/window"
)

// maxEntries caps the entries kept per log; the send budget keeps far fewer.
var maxEntries = 200000

// maxLineBytes is the longest single line read; longer lines cut the file short.
const maxLineBytes = 4 * 1024 * 1024

// maxEntryBytes caps one event with its continuation lines. A runaway stack
// trace would otherwise fill memory, and the send budget keeps far less.
const maxEntryBytes = 64 << 10

// maxKeptBytes caps the text kept in memory for one file while it is read. A
// window full of large events would otherwise grow without a limit, because
// maxEntries only trims after the whole file has been read.
var maxKeptBytes = 64 << 20

// seekThreshold is the file size above which the start offset is found by
// binary search instead of reading from the beginning.
const seekThreshold = 8 << 20

// Source describes one log to read.
type Source struct {
	Name   string
	Path   string
	Format *timefmt.Format
	// Location is used for timestamps that state no zone.
	Location *time.Location
	// ReadCompressed allows reading rotated .gz files.
	ReadCompressed bool
}

// Entry is one log event: a timestamped line plus its continuation lines.
// Text has its timestamp rewritten into the reference zone.
type Entry struct {
	Time time.Time
	Text string
}

// Result is what was read from one source.
type Result struct {
	Source    Source
	Entries   []Entry
	Files     []string
	BytesRead int64
	// Truncated is set when anything in the window was left out: entries over
	// the count or byte limits, continuation lines over maxEntryBytes, or a
	// line longer than maxLineBytes that cut a file short.
	Truncated bool
}

// ErrUnreadable wraps failures to open or read the current log file.
var ErrUnreadable = errors.New("log is not readable")

// ErrNoTimestamps means the log has lines but none carries a timestamp in
// its time_format. Such a log is not sent: without times its window cannot
// be chosen, and an empty excerpt would look like "nothing to see here".
var ErrNoTimestamps = errors.New("no timestamps found")

// Read extracts the window of src. reference is the reference time of the
// window and zone the zone all timestamps are rewritten into.
func Read(src Source, w window.Window, zone *time.Location) (Result, error) {
	if IsPattern(src.Path) {
		return readPattern(src, w, zone)
	}
	result := Result{Source: src}
	files, err := candidateFiles(src)
	if err != nil {
		return result, err
	}
	// kept holds the entries of the files read so far, oldest file first.
	var kept []Entry
	total := 0
	sawText, sawTimestamp := false, false
	for _, file := range files {
		entries, stats, err := readFile(file, src, w, zone)
		if err != nil {
			if file.path == src.Path {
				return result, fmt.Errorf("%w: %s: %v", ErrUnreadable, file.path, err)
			}
			continue // an unreadable rotated file only loses older history
		}
		result.Files = append(result.Files, file.path)
		result.BytesRead += stats.bytesRead
		sawText = sawText || stats.sawText
		result.Truncated = result.Truncated || stats.longLine || stats.trimmed
		sawTimestamp = sawTimestamp || stats.sawTimestamp
		// maxKeptBytes holds for the whole log, not only for each file: many
		// rotated files would otherwise each keep up to the limit. What goes is
		// chosen by time across every file read so far, because an older file
		// by mtime may still hold newer events. Reading goes on after the
		// limit for the same reason.
		kept = append(entries, kept...)
		total += textBytes(entries)
		if total > maxKeptBytes {
			kept, total = dropOldest(kept, total, true)
			result.Truncated = true
		}
		if stats.sawOlderThanWindow {
			break // older rotated files cannot contain anything relevant
		}
	}
	if sawText && !sawTimestamp {
		return result, fmt.Errorf("%w (time_format %s); not sent", ErrNoTimestamps, src.Format.Name)
	}
	result.Entries = kept
	if len(result.Entries) > maxEntries {
		result.Entries = keepNewest(result.Entries, maxEntries)
		result.Truncated = true
	}
	return result, nil
}

// IsPattern reports whether path is a glob such as
// /var/log/tomcat/catalina.*.log, used for logs whose file name carries a
// date or weekday and so has no fixed current file.
func IsPattern(path string) bool { return strings.ContainsAny(path, "*?[") }

// readPattern reads every matching file that may hold part of the window
// and merges the entries by time. The files may be sequential (one per day)
// or written side by side (sssd writes one file per service).
func readPattern(src Source, w window.Window, zone *time.Location) (Result, error) {
	result := Result{Source: src}
	matches, err := filepath.Glob(src.Path)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	var files []file
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		compressed := strings.HasSuffix(match, ".gz")
		if compressed && !src.ReadCompressed {
			continue
		}
		// A file last written before the window cannot contain it.
		if info.ModTime().Before(w.Start) {
			continue
		}
		files = append(files, file{path: match, compressed: compressed, modified: info.ModTime()})
	}
	if len(matches) == 0 {
		return result, fmt.Errorf("%w: no file matches %s", ErrUnreadable, src.Path)
	}
	total := 0
	sawText, sawTimestamp := false, false
	for _, f := range files {
		entries, stats, err := readFile(f, src, w, zone)
		if err != nil {
			return result, fmt.Errorf("%w: %s: %v", ErrUnreadable, f.path, err)
		}
		result.Files = append(result.Files, f.path)
		result.BytesRead += stats.bytesRead
		result.Entries = append(result.Entries, entries...)
		total += textBytes(entries)
		if total > maxKeptBytes {
			// Files matched by a pattern may be written side by side, so the
			// oldest entries are found by time across all of them.
			result.Entries, total = dropOldest(result.Entries, total, true)
			result.Truncated = true
		}
		sawText = sawText || stats.sawText
		result.Truncated = result.Truncated || stats.longLine || stats.trimmed
		sawTimestamp = sawTimestamp || stats.sawTimestamp
	}
	if sawText && !sawTimestamp {
		return result, fmt.Errorf("%w (time_format %s); not sent", ErrNoTimestamps, src.Format.Name)
	}
	sort.SliceStable(result.Entries, func(i, j int) bool { return result.Entries[i].Time.Before(result.Entries[j].Time) })
	if len(result.Entries) > maxEntries {
		result.Entries = result.Entries[len(result.Entries)-maxEntries:]
		result.Truncated = true
	}
	return result, nil
}

func textBytes(entries []Entry) int {
	total := 0
	for _, entry := range entries {
		total += len(entry.Text)
	}
	return total
}

// dropOldest removes entries, oldest by time first, until total is within
// maxKeptBytes, and returns the rest in their original order with the new
// total. With keepOne the newest entry stays even when it alone is over the
// limit, as while reading a file.
func dropOldest(entries []Entry, total int, keepOne bool) ([]Entry, int) {
	order := oldestFirst(entries)
	dropped := make([]bool, len(entries))
	for k, i := range order {
		if total <= maxKeptBytes || (keepOne && k == len(order)-1) {
			break
		}
		total -= len(entries[i].Text)
		dropped[i] = true
	}
	return without(entries, dropped), total
}

// keepNewest keeps the n newest entries by time, in their original order.
func keepNewest(entries []Entry, n int) []Entry {
	dropped := make([]bool, len(entries))
	for _, i := range oldestFirst(entries)[:len(entries)-n] {
		dropped[i] = true
	}
	return without(entries, dropped)
}

// oldestFirst returns the indexes of entries ordered by time. Logs are not
// always written in time order (some write newest first), so the line order
// cannot stand in for age. Equal times keep their line order.
func oldestFirst(entries []Entry) []int {
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return entries[order[a]].Time.Before(entries[order[b]].Time) })
	return order
}

func without(entries []Entry, dropped []bool) []Entry {
	kept := entries[:0]
	for i, entry := range entries {
		if !dropped[i] {
			kept = append(kept, entry)
		}
	}
	// The tail still refers to the dropped texts; clear it so they can be freed.
	clear(entries[len(kept):])
	return kept
}

type file struct {
	path       string
	compressed bool
	modified   time.Time
}

// rotatedSuffix matches logrotate's numbered (.1, .2.gz) and dated
// (-20260927, -20260927.gz) names.
var rotatedSuffix = regexp.MustCompile(`^(\.\d+|-\d{8}(\d{2})?)(\.gz)?$`)

// candidateFiles returns the current file and its rotated siblings, newest
// first by modification time.
func candidateFiles(src Source) ([]file, error) {
	info, err := os.Stat(src.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	files := []file{{path: src.Path, modified: info.ModTime()}}
	// Two globs because "-" inside a character class would denote a range.
	dotted, _ := filepath.Glob(src.Path + ".*")
	dashed, _ := filepath.Glob(src.Path + "-*")
	siblings := append(dotted, dashed...)
	for _, sibling := range siblings {
		suffix := strings.TrimPrefix(sibling, src.Path)
		match := rotatedSuffix.FindStringSubmatch(suffix)
		if match == nil {
			continue
		}
		compressed := match[3] != ""
		if compressed && !src.ReadCompressed {
			continue
		}
		info, err := os.Stat(sibling)
		if err != nil || info.IsDir() {
			continue
		}
		files = append(files, file{path: sibling, compressed: compressed, modified: info.ModTime()})
	}
	sort.SliceStable(files[1:], func(i, j int) bool { return files[1+i].modified.After(files[1+j].modified) })
	return files, nil
}

type fileStats struct {
	bytesRead          int64
	longLine           bool
	sawOlderThanWindow bool
	sawText            bool
	sawTimestamp       bool
	trimmed            bool
}

func readFile(f file, src Source, w window.Window, zone *time.Location) ([]Entry, fileStats, error) {
	var stats fileStats
	handle, err := safeopen.Open(f.path)
	if err != nil {
		return nil, stats, err
	}
	defer handle.Close()

	var reader io.Reader = handle
	if f.compressed {
		gz, err := gzip.NewReader(handle)
		if err != nil {
			return nil, stats, err
		}
		defer gz.Close()
		reader = gz
	} else if info, err := handle.Stat(); err == nil && info.Size() > seekThreshold {
		offset, older := seekStart(handle, info.Size(), src, w.Reference, w.Start)
		stats.sawOlderThanWindow = older
		if _, err := handle.Seek(offset, io.SeekStart); err != nil {
			return nil, stats, err
		}
	}

	return readLines(reader, src, w, zone, stats)
}

// ReadStream extracts the window from text that is not a file, such as the
// output of journalctl (spec 12.7.2). There are no rotated files to look at.
func ReadStream(reader io.Reader, src Source, w window.Window, zone *time.Location) (Result, error) {
	result := Result{Source: src}
	entries, stats, err := readLines(reader, src, w, zone, fileStats{})
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	result.BytesRead = stats.bytesRead
	result.Truncated = stats.longLine || stats.trimmed
	if stats.sawText && !stats.sawTimestamp {
		return result, fmt.Errorf("%w (time_format %s); not sent", ErrNoTimestamps, src.Format.Name)
	}
	result.Entries = entries
	if len(result.Entries) > maxEntries {
		result.Entries = keepNewest(result.Entries, maxEntries)
		result.Truncated = true
	}
	return result, nil
}

// readLines turns lines into entries of the window. stats carries what the
// caller already learned (such as having skipped older lines by seeking).
func readLines(reader io.Reader, src Source, w window.Window, zone *time.Location, stats fileStats) ([]Entry, fileStats, error) {
	counter := &countingReader{reader: reader}
	scanner := bufio.NewScanner(counter)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	var entries []Entry
	// Continuation lines of the last in-window entry, joined once at the end:
	// appending to the entry's string for every line would copy it each time.
	var continuation strings.Builder
	inWindow, dropped, kept := false, 0, 0
	trim := func() {
		if len(entries) <= maxEntries && kept <= maxKeptBytes {
			return
		}
		// Keep the newest half by time: the ranking looks at the end of the
		// window. trim runs only from flush, when no continuation is pending,
		// so any entry may go, including the one read last.
		dropped := make([]bool, len(entries))
		for _, i := range oldestFirst(entries)[:len(entries)/2] {
			kept -= len(entries[i].Text)
			dropped[i] = true
		}
		entries = without(entries, dropped)
		stats.trimmed = true
	}
	flush := func() {
		if len(entries) == 0 {
			return
		}
		last := &entries[len(entries)-1]
		before := len(last.Text)
		last.Text += continuation.String()
		if dropped > 0 {
			last.Text += fmt.Sprintf("\n[jevtri: %d more lines not kept]", dropped)
			stats.trimmed = true
		}
		// Continuation lines count toward the limit too: up to maxEntryBytes
		// per event would otherwise go unaccounted.
		kept += len(last.Text) - before
		continuation.Reset()
		dropped = 0
		trim()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			stats.sawText = true
		}
		m, ok := src.Format.Find(line, src.Location)
		if !ok {
			// A line without a timestamp continues the previous event.
			if inWindow {
				if len(entries[len(entries)-1].Text)+continuation.Len()+1+len(line) <= maxEntryBytes {
					continuation.WriteByte('\n')
					continuation.WriteString(line)
				} else {
					dropped++
				}
			}
			continue
		}
		stats.sawTimestamp = true
		t := timefmt.ResolveYear(m, w.Reference)
		if t.Before(w.Start) {
			stats.sawOlderThanWindow = true
		}
		if inWindow {
			flush()
		}
		if !w.Contains(t) {
			inWindow = false
			continue
		}
		entries = append(entries, Entry{Time: t, Text: timefmt.Rewrite(line, m, t, zone)})
		kept += len(entries[len(entries)-1].Text)
		inWindow = true // flush, at the next timestamp or the end, trims
	}
	if inWindow {
		flush()
	}
	stats.bytesRead = counter.count
	// One line longer than the buffer must not lose the whole log: whoever can
	// write a log could otherwise keep it out of the ranking (SEC-004).
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return entries, stats, err
	} else if err != nil {
		stats.longLine = true
	}
	return entries, stats, nil
}

// seekStart finds, by binary search on byte offsets, an offset at or before
// the first line whose time is at or after start. It assumes the
// file is mostly in time order, which holds for append-only logs.
func seekStart(handle *os.File, size int64, src Source, reference, start time.Time) (int64, bool) {
	low, high := int64(0), size
	sawOlder := false
	for high-low > 64*1024 {
		middle := low + (high-low)/2
		t, ok := firstTimeAfter(handle, middle, src, reference)
		if !ok {
			high = middle
			continue
		}
		if t.Before(start) {
			low, sawOlder = middle, true
		} else {
			high = middle
		}
	}
	return low, sawOlder
}

func firstTimeAfter(handle *os.File, offset int64, src Source, reference time.Time) (time.Time, bool) {
	section := io.NewSectionReader(handle, offset, 256*1024)
	scanner := bufio.NewScanner(section)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	first := true
	for scanner.Scan() {
		if first && offset > 0 {
			first = false
			continue // probably a partial line
		}
		first = false
		if m, ok := src.Format.Find(scanner.Text(), src.Location); ok {
			return timefmt.ResolveYear(m, reference), true
		}
	}
	return time.Time{}, false
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += int64(n)
	return n, err
}
