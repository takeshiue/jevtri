// Package grouprank decides which logs a run ranks when the logs are put in
// groups (spec 12.7.4): with no group named by the user and two or more
// groups of services that have lines in the window, a first stage asks Jev
// which group to examine; the second stage ranks the logs of that group
// together with the logs that are always examined (the system group and logs
// without a group). A group named by the user replaces the first stage.
//
// It knows nothing about Jev or the configuration file, so the way groups
// are used can change without touching the rest of the run.
package grouprank

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// System is the group of the host's own logs; they are always examined.
const System = "system"

// Member is one log that has lines in the window.
type Member struct {
	Name   string
	Groups []string
}

// Plan says what to rank.
type Plan struct {
	// FirstStage is set when Jev must first choose among Candidates.
	FirstStage bool
	// Candidates maps each service group to the names of its logs, for the
	// first stage. Logs that are always examined are not in it.
	Candidates map[string][]string
	// Always are the logs examined whatever the first stage chooses.
	Always []string
	// Direct are the logs to rank at once when there is no first stage.
	Direct []string
}

// Make builds the plan for members. wanted are the groups the user named
// (--group); an unknown name is an error.
func Make(members []Member, wanted []string) (Plan, error) {
	plan := Plan{Candidates: map[string][]string{}}
	for _, m := range members {
		if len(m.Groups) == 0 || contains(m.Groups, System) {
			plan.Always = append(plan.Always, m.Name)
			continue
		}
		for _, g := range m.Groups {
			plan.Candidates[g] = append(plan.Candidates[g], m.Name)
		}
	}
	if len(wanted) > 0 {
		known := map[string]bool{System: true}
		for _, m := range members {
			for _, g := range m.Groups {
				known[g] = true
			}
		}
		for _, g := range wanted {
			if !known[g] {
				return Plan{}, fmt.Errorf("no log with lines in the window is in group %q (groups: %s)", g, strings.Join(Names(members), ", "))
			}
		}
		plan.Direct = Second(plan, wanted)
		plan.Candidates = nil
		return plan, nil
	}
	if len(plan.Candidates) <= 1 {
		// Nothing to choose between: rank everything as before.
		for _, m := range members {
			plan.Direct = append(plan.Direct, m.Name)
		}
		plan.Candidates = nil
		return plan, nil
	}
	plan.FirstStage = true
	return plan, nil
}

// Second returns the logs of the second stage: the logs always examined and
// those of the chosen groups, each once, in the order first seen.
func Second(plan Plan, chosen []string) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, name := range plan.Always {
		add(name)
	}
	for _, g := range chosen {
		for _, name := range plan.Candidates[g] {
			add(name)
		}
	}
	return names
}

// CandidateNames returns the service groups of the first stage, sorted.
func (p Plan) CandidateNames() []string {
	names := make([]string, 0, len(p.Candidates))
	for g := range p.Candidates {
		names = append(names, g)
	}
	sort.Strings(names)
	return names
}

// Names returns every group of members, sorted.
func Names(members []Member) []string {
	set := map[string]bool{}
	for _, m := range members {
		for _, g := range m.Groups {
			set[g] = true
		}
	}
	names := make([]string, 0, len(set))
	for g := range set {
		names = append(names, g)
	}
	sort.Strings(names)
	return names
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Near is how close to the first group's priority (0-1) another group must be
// to go to the second stage too. EV-03 (2026-10-03): taking only the first
// group lost the incident in 2 of 82 runs where two groups tied; taking every
// group within 0.10 lost none, at one more Jev request in 2 of 82 runs.
const Near = 0.10

// Score is a group's first-stage priority (0-1).
type Score struct {
	Group    string
	Priority float64
}

// Choose returns the groups for the second stage: the first one and every
// other within Near of it, in the order given (highest first).
func Choose(ranked []Score) []string {
	if len(ranked) == 0 {
		return nil
	}
	var chosen []string
	cutoff := ranked[0].Priority - Near
	// Allow only arithmetic rounding at the inclusive boundary, not a wider band.
	tolerance := 4 * (math.Nextafter(1, 2) - 1)
	for _, s := range ranked {
		if s.Priority >= cutoff || cutoff-s.Priority <= tolerance {
			chosen = append(chosen, s.Group)
		}
	}
	return chosen
}
