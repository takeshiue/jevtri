package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOtherGroupDistinguishesFirstStageSending(t *testing.T) {
	r := sample()
	r.Logs = append(r.Logs, LogInfo{Name: "other", Path: "/other", Entries: 5, OtherGroup: true, FirstStageEntries: 2, FirstStageBytes: 50})
	r.Logs = append(r.Logs, LogInfo{Name: "unsent", Path: "/unsent", Entries: 5, OtherGroup: true})
	var out bytes.Buffer
	Text(&out, r, true)
	for _, want := range []string{"/other (excerpt sent for group ranking only)", "/unsent (not sent)", "group ranking: sent=2 (50 bytes)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "not examined (not sent") {
		t.Fatal("first-stage excerpt reported as not sent")
	}
	out.Reset()
	if err := JSON(&out, r); err != nil {
		t.Fatal(err)
	}
	var decoded jsonReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.GroupRankingSent) != 1 || decoded.GroupRankingSent[0].Log != "/other" || decoded.GroupRankingSent[0].Entries != 2 {
		t.Fatalf("missing actual stage sending: %+v", decoded)
	}
}

func TestQuietSelectedGroupDoesNotClaimAllLogsQuiet(t *testing.T) {
	r := sample()
	r.Scores = nil
	r.Examined = []string{"quiet"}
	r.Logs = []LogInfo{{Name: "quiet", Path: "/quiet"}, {Name: "active", Path: "/active", Entries: 5, OtherGroup: true}}
	var out bytes.Buffer
	Text(&out, r, false)
	if !strings.Contains(out.String(), "No selected log has entries") || strings.Contains(out.String(), "No configured log has entries") {
		t.Fatalf("misleading quiet report: %s", out.String())
	}
}
