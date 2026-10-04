package logread

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"github.com/takeshiue/jevtri/internal/timefmt"
	"github.com/takeshiue/jevtri/internal/window"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityMaskBeforeWindow(t *testing.T) {
	f, _ := timefmt.Lookup("docker-json")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	var input bytes.Buffer
	body := "synthetic private body"
	for i, payload := range []string{"-----BEGIN PRIVATE KEY-----", body, "-----END PRIVATE KEY-----"} {
		line, _ := json.Marshal(map[string]string{"log": payload + "\n", "stream": "stdout", "time": now.Add(time.Duration(i-2) * time.Second).Format(time.RFC3339Nano)})
		input.Write(line)
		input.WriteByte('\n')
	}
	w := window.Window{Reference: now, Start: now.Add(-time.Second), End: now}
	got, err := ReadStream(&input, Source{Format: f, Location: time.UTC}, w, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries %d", len(got.Entries))
	}
	for _, entry := range got.Entries {
		if strings.Contains(entry.Text, body) {
			t.Fatal("window dropped BEGIN before its mask state")
		}
	}
	if got.Masked["private-key"] == 0 {
		t.Fatal("mask counts missing")
	}
}

func TestSecurityScanCapCompressed(t *testing.T) {
	f, _ := timefmt.Lookup("rfc3339")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	for i := 0; i < 1000; i++ {
		_, _ = z.Write([]byte("2000-01-01T00:00:00Z routine line\n"))
	}
	_ = z.Close()
	if err := os.WriteFile(path+".1.gz", compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(Source{Path: path, Format: f, ReadCompressed: true, Location: time.UTC, MaxScanBytes: 1024}, window.Window{Reference: now, Start: now.Add(-time.Minute), End: now}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.BytesRead > 1025 || got.BytesRead <= 1024 {
		t.Fatalf("limit not reported: %+v", got)
	}
}

func TestSecurityRotationOrphan(t *testing.T) {
	f, _ := timefmt.Lookup("docker-json")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "docker.log")
	body := "U1lOVEhFVElDLVNFQ1JFVC1CT0RZ"
	line, _ := json.Marshal(map[string]string{"log": body + "\n", "time": now.Format(time.RFC3339Nano), "stream": "stdout"})
	if err := os.WriteFile(path, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(Source{Path: path, Format: f, Location: time.UTC}, window.Window{Reference: now, Start: now.Add(-time.Minute), End: now}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || strings.Contains(got.Entries[0].Text, body) {
		t.Fatal("orphan body remains")
	}
}

func TestSecurityMaskBeforeRetention(t *testing.T) {
	previous := maxEntries
	maxEntries = 1
	defer func() { maxEntries = previous }()
	f, _ := timefmt.Lookup("docker-json")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	var input bytes.Buffer
	for _, payload := range []string{"-----BEGIN PRIVATE KEY-----", "short synthetic body"} {
		line, _ := json.Marshal(map[string]string{"log": payload, "time": now.Format(time.RFC3339Nano), "stream": "stdout"})
		input.Write(line)
		input.WriteByte('\n')
	}
	got, err := ReadStream(&input, Source{Format: f, Location: time.UTC}, window.Window{Reference: now, Start: now.Add(-time.Minute), End: now}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || !got.Truncated || strings.Contains(got.Entries[0].Text, "short synthetic body") {
		t.Fatalf("retention removed mask context: %+v", got)
	}
}

func TestSecurityScanDropsPartialLastLine(t *testing.T) {
	f, _ := timefmt.Lookup("rfc3339")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	first := "2026-10-04T00:00:05Z ordinary event\n"
	input := first + "2026-10-04T00:00:05Z partial event must not be kept"
	got, err := ReadStream(strings.NewReader(input), Source{Format: f, Location: time.UTC, MaxScanBytes: int64(len(first) + 27)}, window.Window{Reference: now, Start: now.Add(-time.Minute), End: now}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Entries) != 1 || strings.Contains(got.Entries[0].Text, "partial") {
		t.Fatalf("partial line retained: %+v", got)
	}
}

func TestSecurityOrdinaryRotationBody(t *testing.T) {
	f, _ := timefmt.Lookup("rfc3339")
	now := time.Date(2026, 10, 4, 0, 0, 5, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "app.log")
	body := strings.Repeat("QUJD", 16)
	text := "2026-10-04T00:00:05Z host service: " + body + "\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(Source{Path: path, Format: f, Location: time.UTC}, window.Window{Reference: now, Start: now.Add(-time.Minute), End: now}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || strings.Contains(got.Entries[0].Text, body) || !strings.Contains(got.Entries[0].Text, "host service:") {
		t.Fatal("rotation body or prefix protection failed")
	}
}
