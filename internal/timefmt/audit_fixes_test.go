package timefmt

import (
	"testing"
	"time"
)

func TestInvalidNumericOffsets(t *testing.T) {
	for _, name := range []string{"rfc3339", "iso-space", "iso-comma", "slash-ymd", "dmy-month", "slash-mdy", "slash-dmy"} {
		format, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		dates := map[string]string{"rfc3339": "2026-10-03T12:00:00", "iso-space": "2026-10-03 12:00:00", "iso-comma": "2026-10-03 12:00:00", "slash-ymd": "2026/10/03 12:00:00", "dmy-month": "03-Oct-2026 12:00:00", "slash-mdy": "10/03/2026 12:00:00", "slash-dmy": "03/10/2026 12:00:00"}
		for _, offset := range []string{"+99:99", "+09:99", "-24:00", "+09:0", "+090", "+09:000", "+09000"} {
			if _, ok := format.Find(dates[name]+offset+" failure", time.UTC); ok {
				t.Errorf("%s accepted %s", name, offset)
			}
		}
		for _, offset := range []string{"+23:59", "-23:59", "+0900", "Z", ""} {
			if _, ok := format.Find(dates[name]+offset+" failure", time.UTC); !ok {
				t.Errorf("%s rejected %s", name, offset)
			}
		}
	}
}

func TestCustomMonthRequired(t *testing.T) {
	for _, layout := range []string{"%d %H:%M", "%Y %d %H:%M"} {
		if _, err := Custom(layout); err == nil {
			t.Errorf("accepted incomplete date %s", layout)
		}
	}
	for _, layout := range []string{"%m %d %H:%M", "%b %d %H:%M", "%s"} {
		if _, err := Custom(layout); err != nil {
			t.Errorf("rejected complete date %s: %v", layout, err)
		}
	}
}

func TestExplicitZoneFollowedBySignedMessage(t *testing.T) {
	for _, name := range []string{"rfc3339", "iso-space"} {
		format, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		prefix := "2026-10-03T12:00:00"
		if name == "iso-space" {
			prefix = "2026-10-03 12:00:00"
		}
		for _, suffix := range []string{"Z -1 worker returned", "+0900 +1 worker started", "+09:00 -2 workers stopped"} {
			if _, ok := format.Find(prefix+suffix, time.UTC); !ok {
				t.Errorf("%s rejected %q", name, suffix)
			}
		}
	}
}
