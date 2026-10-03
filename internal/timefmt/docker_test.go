package timefmt

import (
	"testing"
	"time"
)

// TF-05: docker-json reads the "time" field, not a timestamp inside "log",
// and Rewrite changes only the timestamp.
func TestDockerJSON(t *testing.T) {
	format, err := Lookup("docker-json")
	if err != nil {
		t.Fatal(err)
	}
	line := `{"log":"2026-01-01 00:00:00 app started\n","stream":"stdout","time":"2026-10-01T10:00:05.987654321Z"}`
	m, ok := format.Find(line, time.Local)
	if !ok || !m.Time.Equal(time.Date(2026, 10, 1, 10, 0, 5, 987654321, time.UTC)) {
		t.Fatalf("%v %v", m.Time, ok)
	}
	jst := time.FixedZone("JST", 9*3600)
	got := Rewrite(line, m, m.Time, jst)
	want := `{"log":"2026-01-01 00:00:00 app started\n","stream":"stdout","time":"2026-10-01T19:00:05.987+09:00"}`
	if got != want {
		t.Errorf("rewrite:\n got %s\nwant %s", got, want)
	}
}
