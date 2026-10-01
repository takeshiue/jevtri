package timefmt

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// customDirectives maps the % directives accepted in a user-defined
// time_format to regular expression fragments with the same group names as
// the catalog, so both share one assembly path.
var customDirectives = map[byte]string{
	'Y': `(?P<Y>\d{4})`,
	'm': `(?P<m>\d{1,2})`,
	'b': `(?P<monname>` + monthNames + `)`,
	'd': `(?P<d>[ \d]?\d)`,
	'H': `(?P<H>[ \d]?\d)`,
	'M': `(?P<M>\d{2})`,
	'S': `(?P<S>\d{2})`,
	'f': `(?P<frac>\d{1,9})`,
	'z': `(?P<tz>` + numericTZ + `)`,
	'Z': `(?P<tz>` + abbreviationPattern() + `)`,
	'a': `(?:` + dayNames + `)`,
	's': `(?P<epoch>\d{9,11})`,
	'%': `%`,
}

// Custom builds a Format from a strftime-like layout such as
// "%d-%m-%Y %H:%M:%S". Each directive may appear at most once.
func Custom(layout string) (*Format, error) {
	var pattern strings.Builder
	seen := map[byte]bool{}
	for i := 0; i < len(layout); i++ {
		if layout[i] != '%' {
			pattern.WriteString(regexp.QuoteMeta(string(layout[i])))
			continue
		}
		if i+1 >= len(layout) {
			return nil, fmt.Errorf("time_format %q ends with a lone %%", layout)
		}
		directive := layout[i+1]
		fragment, ok := customDirectives[directive]
		if !ok {
			return nil, fmt.Errorf("time_format %q: unsupported directive %%%c", layout, directive)
		}
		if directive != '%' && seen[directive] {
			return nil, fmt.Errorf("time_format %q: %%%c appears twice", layout, directive)
		}
		seen[directive] = true
		pattern.WriteString(fragment)
		i++
	}
	if !seen['s'] && !(seen['H'] && seen['M'] && (seen['d'])) {
		return nil, fmt.Errorf("time_format %q needs at least %%d, %%H and %%M, or %%s", layout)
	}
	compiled, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, fmt.Errorf("time_format %q: %w", layout, err)
	}
	return &Format{Name: layout, pattern: compiled}, nil
}

// Resolve returns a catalog format when name is a catalog name, and
// otherwise treats it as a user-defined layout.
func Resolve(name string) (*Format, error) {
	if format, ok := catalog[name]; ok {
		return format, nil
	}
	if strings.Contains(name, "%") {
		return Custom(name)
	}
	return nil, fmt.Errorf("unknown time format %q (not in the catalog and has no %% directives)", name)
}

// ResolveYear fixes the placeholder year of a match without a year by
// choosing, among the reference year and its neighbours, the one that puts
// the time closest to reference. This handles logs that cross New Year.
func ResolveYear(m Match, reference time.Time) time.Time {
	if m.HasYear {
		return m.Time
	}
	best := m.Time
	bestDistance := time.Duration(-1)
	for _, year := range []int{reference.Year() - 1, reference.Year(), reference.Year() + 1} {
		candidate := time.Date(year, m.Time.Month(), m.Time.Day(), m.Time.Hour(), m.Time.Minute(),
			m.Time.Second(), m.Time.Nanosecond(), m.Time.Location())
		if candidate.Day() != m.Time.Day() {
			continue // Feb 29 in a non-leap year
		}
		distance := candidate.Sub(reference)
		if distance < 0 {
			distance = -distance
		}
		if bestDistance < 0 || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// Rewrite replaces the timestamp text of m in line with t written as
// RFC 3339 in the given location, so that every line sent to Jev uses the
// reference time zone. Mixed zones made Jev misjudge in the 2026-09-28
// experiment.
func Rewrite(line string, m Match, t time.Time, location *time.Location) string {
	return line[:m.Start] + t.In(location).Format("2006-01-02T15:04:05.000Z07:00") + line[m.End:]
}
