package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSecurityTextEscapesAllData(t *testing.T) {
	r := sample()
	control := "value\x1b[2J\nspoof\u009b"
	r.Model = control
	r.Symptom = control
	r.SentLog = control
	r.Examined = []string{control}
	r.Scores[0].Path = control
	r.Logs[0].Name = control
	r.Logs[0].Path = control
	r.Logs[0].TimeFormat = control
	r.Logs[0].Files = []string{control, "another"}
	r.Logs[0].Masked = map[string]int{control: 1}
	r.Logs[2].Skipped = control
	var text bytes.Buffer
	Text(&text, r, true)
	if bytes.ContainsRune(text.Bytes(), 27) || strings.ContainsRune(text.String(), 0x9b) || strings.Contains(text.String(), "\nspoof") {
		t.Fatal("terminal control remains")
	}
	var machine bytes.Buffer
	if err := JSON(&machine, r); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(machine.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["model"] != control || r.Scores[0].Path != control || r.Logs[0].Files[0] != control {
		t.Fatal("text rendering mutated report data")
	}
}
