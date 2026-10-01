// Package budget fits the extracted log entries into the send size limit.
//
// The limit is shared equally between logs; what a small log does not use is
// handed to the others. Within a log, entries that look like problems and
// entries close to the reference time are kept first, and the kept entries
// are returned in time order.
package budget

import (
	"regexp"
	"sort"
	"time"
)

// DefaultLimit is the default total size of log text sent to Jev (spec 12.1).
const DefaultLimit = 48000

// Entry is one log event after masking.
type Entry struct {
	Time time.Time
	Text string
}

// Log is the input for one log.
type Log struct {
	Name    string
	Entries []Entry
}

// Selection is what is sent for one log.
type Selection struct {
	Name    string
	Entries []Entry
	Bytes   int
	Dropped int
	// Cut counts entries shortened because a single event was too long.
	Cut int
}

// problem matches words that suggest an entry reports a failure.
var problem = regexp.MustCompile(`(?i)\b(emerg|alert|crit(ical)?|fatal|severe|error|err|fail(ed|ure)?|denied|refused|panic|segfault|oom|out of memory|killed|timed? ?out|unreachable|exception|abort(ed|ing)?|warn(ing)?)\b`)

const cutMarker = " …[truncated]"

// Fit selects entries so that the total text of all logs stays within limit
// bytes. reference is the incident time used to prefer nearby entries.
func Fit(logs []Log, limit int, reference time.Time) []Selection {
	shares := allocate(logs, limit)
	selections := make([]Selection, len(logs))
	for i, log := range logs {
		selections[i] = pick(log, shares[i], reference)
	}
	return selections
}

// allocate divides limit so that no log gets more than it needs and the rest
// is shared equally (water filling).
func allocate(logs []Log, limit int) []int {
	needs := make([]int, len(logs))
	for i, log := range logs {
		for _, entry := range log.Entries {
			needs[i] += len(entry.Text) + 1
		}
	}
	shares := make([]int, len(logs))
	remaining := limit
	open := make([]int, 0, len(logs))
	for i := range logs {
		open = append(open, i)
	}
	for len(open) > 0 && remaining > 0 {
		equal := remaining / len(open)
		if equal == 0 {
			break
		}
		var next []int
		for _, i := range open {
			if needs[i]-shares[i] <= equal {
				remaining -= needs[i] - shares[i]
				shares[i] = needs[i]
			} else {
				next = append(next, i)
			}
		}
		if len(next) == len(open) {
			for _, i := range open {
				shares[i] += equal
				remaining -= equal
			}
			break
		}
		open = next
	}
	return shares
}

func pick(log Log, share int, reference time.Time) Selection {
	selection := Selection{Name: log.Name}
	order := make([]int, len(log.Entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ea, eb := log.Entries[order[a]], log.Entries[order[b]]
		pa, pb := problem.MatchString(ea.Text), problem.MatchString(eb.Text)
		if pa != pb {
			return pa
		}
		return distance(ea.Time, reference) < distance(eb.Time, reference)
	})
	keep := map[int]string{}
	used := 0
	for _, i := range order {
		text := log.Entries[i].Text
		size := len(text) + 1
		if used+size > share {
			room := share - used - 1 - len(cutMarker)
			// Shorten one long event (a stack trace) rather than lose it, but
			// only when a meaningful part fits.
			if room >= 200 {
				keep[i] = truncate(text, room) + cutMarker
				used += len(keep[i]) + 1
				selection.Cut++
			}
			continue
		}
		keep[i] = text
		used += size
	}
	for i, entry := range log.Entries {
		text, ok := keep[i]
		if !ok {
			selection.Dropped++
			continue
		}
		selection.Entries = append(selection.Entries, Entry{Time: entry.Time, Text: text})
	}
	sort.SliceStable(selection.Entries, func(a, b int) bool { return selection.Entries[a].Time.Before(selection.Entries[b].Time) })
	selection.Bytes = used
	return selection
}

func distance(t, reference time.Time) time.Duration {
	d := t.Sub(reference)
	if d < 0 {
		return -d
	}
	return d
}

// truncate cuts text to at most n bytes without splitting a UTF-8 sequence.
func truncate(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && text[n]&0xC0 == 0x80 {
		n--
	}
	return text[:n]
}
