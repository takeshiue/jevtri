package setup

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/takeshiue/jevtri/internal/config"
)

func TestCompleteExistingCustomMaskProtectsAllSurfaces(t *testing.T) {
	const canary = "CUSTOM-CANARY-7301"
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": "=2026-09-28 03:02:30 ERROR " + canary + " password=Fixture-Secret-42\n"})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	original := "[log app]\npath = /opt/app/app.log\nmask = CUSTOM-CANARY-[0-9]+ # preserve custom rule\n"
	if err := os.WriteFile(opts.ConfigPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := completeExisting(bufio.NewReader(strings.NewReader("y\n")), &output, opts); err != nil {
		t.Fatal(err)
	}
	if len(detector.requests) != 1 {
		t.Fatalf("requests=%d", len(detector.requests))
	}
	record, err := os.ReadFile(opts.SentLog)
	if err != nil {
		t.Fatal(err)
	}
	for name, surface := range map[string]string{"preview": output.String(), "request": detector.requests[0].State.Lines, "record": string(record)} {
		if strings.Contains(surface, canary) || strings.Contains(surface, "Fixture-Secret-42") || !strings.Contains(surface, "MASKED:custom") || !strings.Contains(surface, "MASKED:secret") {
			t.Errorf("%s did not protect custom/default secrets", name)
		}
	}
	updated, err := os.ReadFile(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "mask = CUSTOM-CANARY-[0-9]+ # preserve custom rule") {
		t.Fatal("custom mask/comment lost")
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil || cfg.Logs[0].TimeFormat != "iso-space" || len(cfg.Logs[0].Masks) != 1 {
		t.Fatalf("config verification: %v", err)
	}
	info, _ := os.Stat(opts.ConfigPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config mode changed")
	}
}

func TestDetectInvalidCustomMaskStopsBeforeRequest(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": appLine})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	loads := 0
	opts.LoadAPIKey = func() (string, error) { loads++; return "fixture", nil }
	logs := []*Candidate{{Path: "/opt/app/app.log"}, {Path: "/opt/app/app.log", Masks: []string{"["}}}
	var output bytes.Buffer
	err := detectWithJev(bufio.NewReader(strings.NewReader("y\n")), &output, logs, opts)
	if err == nil || !strings.Contains(err.Error(), "invalid mask") || loads != 0 || len(detector.requests) != 0 || output.Len() != 0 {
		t.Fatalf("invalid regex did not stop before preview/load/send: err=%v loads=%d requests=%d", err, loads, len(detector.requests))
	}
	if _, err := os.Stat(opts.SentLog); !os.IsNotExist(err) {
		t.Fatal("invalid regex created send record")
	}
	for _, candidate := range logs {
		if candidate.TimeFormat != "" {
			t.Fatal("invalid mask changed format")
		}
	}
}

func TestHeadLinesMasksBeforeTruncation(t *testing.T) {
	const canary = "CROSS-BOUNDARY-CANARY"
	line := "2026-09-28 03:02:30 " + canary + strings.Repeat("x", maxLineLength) + "-END\n"
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": "=" + line})
	lines := headLines(root, "/opt/app/app.log", canary+"x+-END")
	if len(lines) != 1 || strings.Contains(lines[0], canary) || !strings.Contains(lines[0], "MASKED:custom") || len(lines[0]) > maxLineLength {
		t.Fatal("mask did not run before truncation")
	}
}

func TestCompleteFormatDuplicateComments(t *testing.T) {
	path := t.TempDir() + "/config"
	if err := os.WriteFile(path, []byte("[log app]\npath = /opt/app/app.log\ntime_format = # first comment\ntime_format = # second comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := updateTimeFormats(path, map[string]string{"app": "iso-space"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "time_format =") != 1 || !strings.Contains(string(data), "# first comment") || !strings.Contains(string(data), "# second comment") {
		t.Fatal("duplicate format/comment preservation failed")
	}
}

func TestSamePathCandidatesPreserveIndependentMasks(t *testing.T) {
	const first = "FIRST-CUSTOM-CANARY"
	const second = "SECOND-CUSTOM-CANARY"
	root := fakeRoot(t, ubuntu, map[string]string{"/opt/app/app.log": "=2026-09-28 03:02:30 ERROR " + first + " " + second + "\n"})
	detector := &fakeDetector{answer: "iso-space"}
	opts := jevOptions(t, detector, nil)
	opts.Root = root
	original := "[log first]\npath = /opt/app/app.log\nmask = " + first + "\n[log second]\npath = /opt/app/app.log\nmask = " + second + "\n"
	if err := os.WriteFile(opts.ConfigPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := completeExisting(bufio.NewReader(strings.NewReader("y\n")), &output, opts); err != nil {
		t.Fatal(err)
	}
	if len(detector.requests) != 2 {
		t.Fatalf("requests=%d", len(detector.requests))
	}
	previews := strings.Split(output.String(), "\n  /opt/app/app.log:\n")
	if len(previews) != 3 {
		t.Fatal("expected two independent previews")
	}
	record, err := os.ReadFile(opts.SentLog)
	if err != nil {
		t.Fatal(err)
	}
	records := strings.Split(strings.TrimSpace(string(record)), "\n")
	if len(records) != 2 {
		t.Fatal("expected two independent records")
	}
	for index, pair := range [][2]string{{first, second}, {second, first}} {
		for name, surface := range map[string]string{"preview": previews[index+1], "request": detector.requests[index].State.Lines, "record": records[index]} {
			if strings.Contains(surface, pair[0]) || !strings.Contains(surface, pair[1]) || !strings.Contains(surface, "MASKED:custom") {
				t.Errorf("candidate %d %s did not preserve its individual mask", index, name)
			}
		}
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil || len(cfg.Logs) != 2 {
		t.Fatalf("updated config: %v", err)
	}
	for index, canary := range []string{first, second} {
		if cfg.Logs[index].Masks[0] != canary || cfg.Logs[index].TimeFormat != "iso-space" {
			t.Errorf("candidate %d configuration changed", index)
		}
	}
}
