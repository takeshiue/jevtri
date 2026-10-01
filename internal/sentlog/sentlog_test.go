package sentlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

// RC-01: one line per run, 0600, no key.
func TestAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jevtri", "sent.log")
	now := time.Date(2026, 9, 28, 15, 47, 0, 0, time.UTC)
	request := jev.BuildRequest(jev.Query{Reference: now, WindowStart: now, WindowEnd: now, Minutes: 5,
		Logs: []jev.Log{{Path: "/var/log/syslog", Text: "line"}}})
	result := &jev.Result{Model: "jev-1.13.0", Scores: []jev.Score{{Path: "/var/log/syslog", Raw: 2.5}}, Latency: 240 * time.Millisecond}

	if err := Append(path, Entry(now, request, false, result, nil)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, Entry(now, request, true, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, Entry(now, request, false, nil, errors.New("jev could not be reached"))); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", dirInfo.Mode().Perm())
	}
	handle, _ := os.Open(path)
	defer handle.Close()
	scanner := bufio.NewScanner(handle)
	var records []Record
	for scanner.Scan() {
		if strings.Contains(strings.ToLower(scanner.Text()), "authorization") || strings.Contains(scanner.Text(), "Bearer") {
			t.Error("the send log must not contain credentials")
		}
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) != 3 || records[0].Model != "jev-1.13.0" || !records[1].DryRun || records[2].Error == "" {
		t.Errorf("unexpected records %+v", records)
	}
	recorded, _ := json.Marshal(records[0].Request)
	if !strings.Contains(string(recorded), `"logs":{"/var/log/syslog":"line"}`) {
		t.Error("the request body must be recorded as sent")
	}
}

func TestLoosePermissionsAreTightened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.log")
	os.WriteFile(path, nil, 0o644)
	os.Chmod(path, 0o644)
	if err := Append(path, Record{Time: "x"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %o, want 600", info.Mode().Perm())
	}
}

func TestFormatEntry(t *testing.T) {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	request := jev.BuildFormatRequest("/opt/app/app.log", "masked line", []jev.FormatChoice{{Name: "syslog", Example: "Sep 28 07:51:24"}})
	result := &jev.FormatResult{Model: "jev-1.13.0", Options: []jev.Option{{Name: "syslog", Probability: 0.9}, {Name: "none", Probability: 0.1}}}
	line, err := json.Marshal(FormatEntry(now, request, result, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"purpose":"time_format"`, `"lines":"masked line"`, `"options":[{"name":"syslog","probability":0.9}`} {
		if !strings.Contains(string(line), want) {
			t.Errorf("record lacks %s: %s", want, line)
		}
	}
}
