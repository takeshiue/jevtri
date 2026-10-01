package budget

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var reference = time.Date(2026, 9, 28, 15, 47, 0, 0, time.UTC)

func routine(name string, count, size int) Log {
	log := Log{Name: name}
	for i := 0; i < count; i++ {
		text := fmt.Sprintf("%s routine line %d %s", name, i, strings.Repeat("x", size))
		log.Entries = append(log.Entries, Entry{Time: reference.Add(time.Duration(i-count/2) * time.Second), Text: text})
	}
	return log
}

func total(selections []Selection) int {
	sum := 0
	for _, s := range selections {
		sum += s.Bytes
	}
	return sum
}

// SZ-01: the total stays within the limit and error lines survive.
func TestLimitAndPriority(t *testing.T) {
	logs := []Log{routine("a", 2000, 100), routine("b", 2000, 100), routine("c", 2000, 100), routine("d", 2000, 100), routine("e", 2000, 100)}
	far := reference.Add(-4 * time.Minute)
	logs[2].Entries = append(logs[2].Entries, Entry{Time: far, Text: "c [error] connect() failed (111: Connection refused) while connecting to upstream"})
	selections := Fit(logs, DefaultLimit, reference)
	if got := total(selections); got > DefaultLimit {
		t.Fatalf("total %d exceeds %d", got, DefaultLimit)
	}
	found := false
	for _, entry := range selections[2].Entries {
		if strings.Contains(entry.Text, "Connection refused") {
			found = true
		}
	}
	if !found {
		t.Error("the error line far from the reference time was dropped")
	}
	for i := 1; i < len(selections[2].Entries); i++ {
		if selections[2].Entries[i].Time.Before(selections[2].Entries[i-1].Time) {
			t.Fatal("entries are not in time order")
		}
	}
}

// A small log does not waste its share; the others receive it.
func TestUnusedShareIsRedistributed(t *testing.T) {
	logs := []Log{routine("small", 3, 10), routine("big1", 5000, 100), routine("big2", 5000, 100)}
	selections := Fit(logs, 30000, reference)
	if selections[0].Dropped != 0 {
		t.Errorf("small log lost %d entries", selections[0].Dropped)
	}
	if selections[1].Bytes < 14000 || selections[2].Bytes < 14000 {
		t.Errorf("big logs got %d and %d bytes; expected about half of the rest each", selections[1].Bytes, selections[2].Bytes)
	}
	if got := total(selections); got > 30000 {
		t.Errorf("total %d exceeds limit", got)
	}
}

// A single long event (a stack trace) is shortened, not lost.
func TestLongEventIsCut(t *testing.T) {
	trace := "SEVERE Exception in thread main java.lang.OutOfMemoryError: Java heap space" + strings.Repeat("\n\tat com.example.Service.method(Service.java:42)", 400)
	logs := []Log{{Name: "tomcat", Entries: []Entry{{Time: reference, Text: trace}}}}
	selections := Fit(logs, 5000, reference)
	if len(selections[0].Entries) != 1 || selections[0].Cut != 1 {
		t.Fatalf("got %+v", selections[0])
	}
	text := selections[0].Entries[0].Text
	if !strings.HasPrefix(text, "SEVERE Exception") || !strings.HasSuffix(text, cutMarker) || len(text)+1 > 5000 {
		t.Errorf("unexpected cut: %d bytes", len(text))
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	text := "エラー: 接続できません"
	for n := 0; n <= len(text); n++ {
		if !utf8.ValidString(truncate(text, n)) {
			t.Fatalf("truncate(%d) broke UTF-8", n)
		}
	}
}
