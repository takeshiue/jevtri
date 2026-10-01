// Package timefmt finds and parses timestamps in log lines.
//
// Each catalog format is read leniently: fractional seconds are optional,
// the zone may be written as +09:00, +0900, Z, a known abbreviation or be
// absent, and a single-digit hour may be space padded. The timestamp does not
// have to start the line; the first match in the line is used.
package timefmt

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Format locates a timestamp in a line and converts it to an instant.
type Format struct {
	Name    string
	pattern *regexp.Regexp
}

// Match is a timestamp found in a line.
type Match struct {
	Time time.Time
	// Start and End are byte offsets of the timestamp text in the line.
	Start, End int
	// HasYear is false for formats such as syslog that omit the year; the
	// year of Time is then a placeholder that the caller must resolve.
	HasYear bool
	// HasZone reports whether the line itself stated the zone.
	HasZone bool
}

const (
	monthNames = `Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec`
	dayNames   = `Mon|Tue|Wed|Thu|Fri|Sat|Sun`
	clock      = `(?P<H>[ \d]?\d):(?P<M>\d{2}):(?P<S>\d{2})(?:[.,](?P<frac>\d{1,9}))?`
	numericTZ  = `Z|[+-]\d{2}:?\d{2}`
)

// zoneAbbreviations are the abbreviations accepted as a zone. Ambiguous ones
// such as CST or IST are left out on purpose; they fall back to the default.
var zoneAbbreviations = map[string]int{
	"UTC": 0, "GMT": 0,
	"JST": 9 * 3600, "KST": 9 * 3600, "HKT": 8 * 3600, "SGT": 8 * 3600,
	"EST": -5 * 3600, "EDT": -4 * 3600, "MST": -7 * 3600, "MDT": -6 * 3600,
	"PST": -8 * 3600, "PDT": -7 * 3600,
	"CET": 1 * 3600, "CEST": 2 * 3600, "EET": 2 * 3600, "EEST": 3 * 3600,
}

func abbreviationPattern() string {
	names := make([]string, 0, len(zoneAbbreviations))
	for name := range zoneAbbreviations {
		names = append(names, name)
	}
	// Longer names first so that CEST wins over CET.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return strings.Join(names, "|")
}

func catalogPatterns() map[string]string {
	anyTZ := `(?:\s*(?P<tz>` + numericTZ + `|(?:` + abbreviationPattern() + `)\b))?`
	return map[string]string{
		"syslog":        `\b(?P<monname>` + monthNames + `)\s+(?P<d>\d{1,2})\s+` + clock,
		"rfc3339":       `\b(?P<Y>\d{4})-(?P<m>\d{2})-(?P<d>\d{2})T` + clock + `(?P<tz>` + numericTZ + `)?`,
		"iso-space":     `\b(?P<Y>\d{4})-(?P<m>\d{2})-(?P<d>\d{2})\s{1,2}` + clock + anyTZ,
		"iso-comma":     `\b(?P<Y>\d{4})-(?P<m>\d{2})-(?P<d>\d{2})\s{1,2}` + clock + anyTZ,
		"slash-ymd":     `\b(?P<Y>\d{4})/(?P<m>\d{2})/(?P<d>\d{2})\s+` + clock + anyTZ,
		"apache-access": `\[(?P<d>\d{2})/(?P<monname>` + monthNames + `)/(?P<Y>\d{4}):` + clock + `(?:\s+(?P<tz>` + numericTZ + `))?\]`,
		"apache-error":  `\[?(?:` + dayNames + `)\s+(?P<monname>` + monthNames + `)\s+(?P<d>\d{1,2})\s+` + clock + `\s+(?P<Y>\d{4})\]?`,
		"dmy-month":     `\b(?P<d>\d{1,2})[- ](?P<monname>` + monthNames + `)[- ](?P<Y>\d{4})[ :]` + clock + anyTZ,
		"slash-mdy":     `\b(?P<m>\d{2})/(?P<d>\d{2})/(?P<Y>\d{4})[ T:]` + clock + anyTZ,
		"slash-dmy":     `\b(?P<d>\d{2})/(?P<m>\d{2})/(?P<Y>\d{4})[ T:]` + clock + anyTZ,
		"epoch":         `\b(?P<epoch>1\d{9})(?:\.(?P<frac>\d{1,9}))?\b`,
	}
}

var catalog = func() map[string]*Format {
	formats := map[string]*Format{}
	for name, pattern := range catalogPatterns() {
		formats[name] = &Format{Name: name, pattern: regexp.MustCompile(pattern)}
	}
	return formats
}()

// Names returns the catalog format names in a stable order.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup returns a catalog format by name.
func Lookup(name string) (*Format, error) {
	format, ok := catalog[name]
	if !ok {
		return nil, fmt.Errorf("unknown time format %q", name)
	}
	return format, nil
}

// Find returns the first timestamp in line. Times without a stated zone are
// interpreted in defaultLocation.
func (f *Format) Find(line string, defaultLocation *time.Location) (Match, bool) {
	indexes := f.pattern.FindStringSubmatchIndex(line)
	if indexes == nil {
		return Match{}, false
	}
	groups := map[string]string{}
	for i, name := range f.pattern.SubexpNames() {
		if name == "" || indexes[2*i] < 0 {
			continue
		}
		groups[name] = line[indexes[2*i]:indexes[2*i+1]]
	}
	t, hasYear, hasZone, err := assemble(groups, defaultLocation)
	if err != nil {
		return Match{}, false
	}
	return Match{Time: t, Start: indexes[0], End: indexes[1], HasYear: hasYear, HasZone: hasZone}, true
}

func assemble(groups map[string]string, defaultLocation *time.Location) (time.Time, bool, bool, error) {
	nanos, err := fraction(groups["frac"])
	if err != nil {
		return time.Time{}, false, false, err
	}
	if epoch, ok := groups["epoch"]; ok {
		seconds, err := strconv.ParseInt(epoch, 10, 64)
		if err != nil {
			return time.Time{}, false, false, err
		}
		return time.Unix(seconds, int64(nanos)).In(defaultLocation), true, true, nil
	}

	year, hasYear := 0, false
	if value, ok := groups["Y"]; ok {
		year, hasYear = atoi(value), true
	}
	month := 0
	if value, ok := groups["m"]; ok {
		month = atoi(value)
	} else if value, ok := groups["monname"]; ok {
		month = monthNumber(value)
	}
	day, hour, minute, second := atoi(groups["d"]), atoi(groups["H"]), atoi(groups["M"]), atoi(groups["S"])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 60 {
		return time.Time{}, false, false, fmt.Errorf("timestamp out of range")
	}

	location, hasZone, err := zone(groups["tz"], defaultLocation)
	if err != nil {
		return time.Time{}, false, false, err
	}
	if !hasYear {
		// Placeholder; resolved later against the reference time.
		year = 2000
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, nanos, location)
	if t.Day() != day {
		return time.Time{}, false, false, fmt.Errorf("invalid date")
	}
	return t, hasYear, hasZone, nil
}

func zone(text string, defaultLocation *time.Location) (*time.Location, bool, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return defaultLocation, false, nil
	case text == "Z":
		return time.UTC, true, nil
	case text[0] == '+' || text[0] == '-':
		digits := strings.ReplaceAll(text[1:], ":", "")
		if len(digits) != 4 {
			return nil, false, fmt.Errorf("bad zone offset %q", text)
		}
		offset := atoi(digits[:2])*3600 + atoi(digits[2:])*60
		if text[0] == '-' {
			offset = -offset
		}
		return time.FixedZone(text, offset), true, nil
	default:
		offset, ok := zoneAbbreviations[text]
		if !ok {
			return defaultLocation, false, nil
		}
		return time.FixedZone(text, offset), true, nil
	}
}

func fraction(digits string) (int, error) {
	if digits == "" {
		return 0, nil
	}
	padded := (digits + "000000000")[:9]
	return strconv.Atoi(padded)
}

func monthNumber(name string) int {
	for i, candidate := range strings.Split(monthNames, "|") {
		if candidate == name {
			return i + 1
		}
	}
	return 0
}

func atoi(text string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(text))
	return value
}
