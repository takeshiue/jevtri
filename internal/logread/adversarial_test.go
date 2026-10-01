//go:build adversarial

package logread

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/window"
)

// Adversarial checks AD-10 to AD-13 (test plan "例外入力による確認").

var adBase = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func adWindow() window.Window {
	return window.Window{Reference: adBase, Start: adBase.Add(-time.Hour), End: adBase.Add(time.Hour)}
}

func adSource(t *testing.T, path string) Source {
	return Source{Name: "ad", Path: path, Format: mustFormat(t, "rfc3339"), Location: time.UTC}
}

func keptBytes(entries []Entry) int {
	total := 0
	for _, e := range entries {
		total += len(e.Text)
	}
	return total
}

// events writes n events, each a first line and a continuation of size bytes.
func events(n, size int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(adBase.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano))
		b.WriteString(" event\n")
		if size > 0 {
			b.WriteString(strings.Repeat("x", size))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// AD-10: limits smaller than one event, zero, and exactly one event.
func TestAD10LimitEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.log")
	os.WriteFile(path, []byte(events(50, 4000)), 0o600)
	one := len(adBase.Format(time.RFC3339Nano)) + len(" event\n") + 4000
	saved := maxKeptBytes
	defer func() { maxKeptBytes = saved }()
	for _, limit := range []int{0, 1, one / 2, one, one * 3} {
		maxKeptBytes = limit
		result, err := Read(adSource(t, path), adWindow(), time.UTC)
		if err != nil {
			t.Errorf("limit %d: %v", limit, err)
			continue
		}
		kept := keptBytes(result.Entries)
		// The kept text is rewritten (timestamps into the reference zone), so
		// one event is measured from what Read returned.
		for _, e := range result.Entries {
			if len(e.Text) > one {
				one = len(e.Text)
			}
		}
		if kept > limit+one {
			t.Errorf("limit %d: kept %d bytes, over limit plus one event (%d)", limit, kept, limit+one)
		}
		if len(result.Entries) == 0 || !strings.HasSuffix(result.Entries[len(result.Entries)-1].Text, "xxxx") {
			t.Errorf("limit %d: the newest event or its continuation was lost", limit)
		}
		t.Logf("limit %d: %d entries, %d bytes, truncated %v", limit, len(result.Entries), kept, result.Truncated)
	}
}

// AD-11: the limit applies per file; many files may add up past it.
func TestAD11ManyFiles(t *testing.T) {
	saved := maxKeptBytes
	maxKeptBytes = 64 << 10
	defer func() { maxKeptBytes = saved }()
	body := events(30, 2000) // about 62 KiB, just under the limit
	modified := adBase

	rotated := t.TempDir()
	current := filepath.Join(rotated, "app.log")
	write(t, current, body, modified)
	for i := 1; i <= 20; i++ {
		write(t, current+"."+itoa(i), body, modified.Add(-time.Duration(i)*time.Second))
	}
	pattern := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(pattern, "svc"+itoa(i)+".log"), body, modified)
	}
	for name, path := range map[string]string{"rotated": current, "pattern": filepath.Join(pattern, "svc*.log")} {
		result, err := Read(adSource(t, path), adWindow(), time.UTC)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		kept := keptBytes(result.Entries)
		t.Logf("%s: %d files, %d entries, %d bytes kept", name, len(result.Files), len(result.Entries), kept)
		if kept > maxKeptBytes {
			t.Errorf("%s: kept %d bytes over the %d limit across files", name, kept, maxKeptBytes)
		}
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/10)) + string(rune('0'+i%10)))
}

// AD-12: malformed lines must not crash or lose in-window events.
func TestAD12MalformedLines(t *testing.T) {
	stamp := adBase.Format(time.RFC3339)
	cases := map[string]struct {
		body      string
		wantMin   int
		truncated bool
	}{
		"continuation only":  {strings.Repeat("no timestamp here\n", 1000), 0, false},
		"blank only":         {strings.Repeat("\n", 1000), 0, false},
		"crlf":               {stamp + " a\r\n" + stamp + " b\r\n", 2, false},
		"nul and control":    {stamp + " a\x00b\x1b[31m\x07\n" + stamp + " c\n", 2, false},
		"invalid utf8":       {stamp + " \xff\xfe\xfd\n" + stamp + " ok\n", 2, false},
		"overlong line":      {stamp + " a\n" + strings.Repeat("y", 5<<20) + "\n" + stamp + " after\n", 1, true},
		"reverse order":      {adBase.Add(time.Minute).Format(time.RFC3339) + " late\n" + stamp + " early\n", 2, false},
		"same time many":     {strings.Repeat(stamp+" same\n", 50000), 50000, false},
		"window edges":       {adBase.Add(-time.Hour).Format(time.RFC3339) + " start\n" + adBase.Add(time.Hour).Format(time.RFC3339) + " end\n", 1, false},
		"no newline at end":  {stamp + " last", 1, false},
		"timestamp mid-line": {"prefix " + stamp + " x\n", 0, false},
		"year 0 and 9999":    {"0000-01-01T00:00:00Z a\n9999-12-31T23:59:59Z b\n" + stamp + " c\n", 1, false},
	}
	for name, c := range cases {
		path := filepath.Join(t.TempDir(), "bad.log")
		os.WriteFile(path, []byte(c.body), 0o600)
		result, err := func() (r Result, err error) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("%s: panic: %v", name, p)
				}
			}()
			return Read(adSource(t, path), adWindow(), time.UTC)
		}()
		t.Logf("%s: %d entries, truncated %v, err %v", name, len(result.Entries), result.Truncated, err)
		if len(result.Entries) < c.wantMin {
			t.Errorf("%s: %d entries, want at least %d", name, len(result.Entries), c.wantMin)
		}
		if c.truncated && !result.Truncated {
			t.Errorf("%s: Truncated not set", name)
		}
	}
}

// AD-13: a large log keeps memory near the limit and time proportional.
func TestAD13LargeLog(t *testing.T) {
	dir := t.TempDir()
	measure := func(megabytes int) (time.Duration, uint64, int) {
		path := filepath.Join(dir, "large.log")
		handle, _ := os.Create(path)
		chunk := events(1000, 1000) // about 1 MB
		for i := 0; i < megabytes; i++ {
			handle.WriteString(chunk)
		}
		handle.Close()
		runtime.GC()
		var peak atomic.Uint64
		stop := make(chan struct{})
		go func() {
			var m runtime.MemStats
			for {
				select {
				case <-stop:
					return
				default:
				}
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
		start := time.Now()
		result, err := Read(adSource(t, path), adWindow(), time.UTC)
		elapsed := time.Since(start)
		close(stop)
		if err != nil {
			t.Fatal(err)
		}
		kept := keptBytes(result.Entries)
		os.Remove(path)
		return elapsed, peak.Load(), kept
	}
	smallTime, smallPeak, smallKept := measure(50)
	largeTime, largePeak, largeKept := measure(300)
	t.Logf("50 MB: %v, peak heap %d MiB, kept %d MiB", smallTime, smallPeak>>20, smallKept>>20)
	t.Logf("300 MB: %v, peak heap %d MiB, kept %d MiB", largeTime, largePeak>>20, largeKept>>20)
	if largePeak > 160<<20 {
		t.Errorf("peak heap %d MiB over 160 MiB (limit 64 MiB plus working memory)", largePeak>>20)
	}
	if largeTime > smallTime*6*2 {
		t.Errorf("300 MB took %v, over twice the proportional time from 50 MB (%v)", largeTime, smallTime)
	}
}
