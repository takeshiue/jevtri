package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/containers"
	"github.com/takeshiue/jevtri/internal/docker"
	"github.com/takeshiue/jevtri/internal/journal"
	"github.com/takeshiue/jevtri/internal/logread"
)

// Update is 'jevtri --config-update' (spec 12.7.3): it looks for logs that
// the configuration does not have yet and asks about each one, then asks
// about configured logs whose source is gone. Only the chosen sections are
// added or removed; every other line stays as it is.
func Update(in io.Reader, out io.Writer, opts Options) error {
	cfg, err := config.Load(opts.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s does not exist; run 'jevtri init' first", opts.ConfigPath)
	}
	if err != nil {
		return err
	}
	if opts.SentLog == "" {
		opts.SentLog = cfg.SentLog
	}
	osRelease, _ := os.ReadFile(filepath.Join(opts.Root, "/etc/os-release"))
	family := DetectFamily(string(osRelease))
	inventory, discoveryError := docker.List(opts.Root)
	if discoveryError != nil {
		fmt.Fprintf(out, "Docker discovery is unavailable: %v. Existing Docker registrations are kept; enter absolute paths manually.\n", discoveryError)
	}
	showInventoryIssues(out, inventory)
	manualAvailable := discoveryError != nil
	for _, container := range inventory {
		if container.LogError != nil {
			manualAvailable = true
		}
	}
	reader := bufio.NewReader(in)
	changes, err := chooseDockerChanges(reader, out, cfg, inventory, opts)
	if err != nil {
		return err
	}
	fresh := newCandidates(findWithOffers(family, opts.Root, containers.FromInventory(inventory, opts.Root)), cfg)
	gone := missingLogs(cfg, opts.Root)
	if discoveryError == nil {
		for _, entry := range cfg.Logs {
			if entry.Path != "" || (entry.DockerContainer == "" && entry.DockerProject == "") {
				continue
			}
			present := false
			for _, container := range inventory {
				if (entry.DockerContainer != "" && entry.DockerContainer == container.Name) || (entry.DockerContainer == "" && entry.DockerProject == container.Project) {
					present = true
					break
				}
			}
			if !present {
				gone = append(gone, goneLog{log: entry, reason: "Docker discovery succeeded but this registration was not found; a manual path can also be registered"})
			}
		}
	}
	filteredGone := gone[:0]
	for _, item := range gone {
		if _, changing := changes.Paths[item.log.Name]; changing {
			continue
		}
		if _, changing := changes.Projects[item.log.Name]; changing {
			continue
		}
		if discoveryError != nil && (item.log.DockerContainer != "" || item.log.DockerProject != "") {
			continue
		}
		filteredGone = append(filteredGone, item)
	}
	gone = filteredGone
	if !manualAvailable && len(fresh) == 0 && len(gone) == 0 && len(groupless(cfg, nil, inventory)) == 0 && len(changes.Paths) == 0 && len(changes.Projects) == 0 && len(changes.Formats) == 0 {
		removed := chooseRegisteredRemovals(reader, out, cfg, nil)
		if len(removed) > 0 {
			pruneRemovedChanges(removed, nil, changes)
			if err := saveRegisteredUpdate(opts.ConfigPath, removed, nil, nil, changes, opts.Root); err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed %s from %s.\n", count(len(removed), "log"), opts.ConfigPath)
			deleteRemovedHostLogs(reader, out, cfg, removed, inventory, discoveryError, opts)
			return nil
		}
		if err := ensureRegisteredSample(opts.ConfigPath, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "No registered log changes were needed in %s.\n", opts.ConfigPath)
		return nil
	}

	var chosen []Candidate
	if len(fresh) > 0 {
		fmt.Fprintf(out, "Found %d logs that are not in %s.\n", len(fresh), opts.ConfigPath)
		fmt.Fprintln(out, "Logs you add are sent to Jev (masked) on every run.")
		labels := make([]string, len(fresh))
		for i, c := range fresh {
			labels[i] = fmt.Sprintf("%s (%s)", c.Label(), describeFormat(c))
		}
		for _, i := range askEach(reader, out, labels, "Add it?") {
			chosen = append(chosen, fresh[i])
		}
	}
	// A project cannot preserve individual settings or their section comments.
	// Validate the whole update before reporting removals or sending samples.
	protected, err := migrationSettings(opts.ConfigPath)
	if err != nil {
		return err
	}
	for _, candidate := range chosen {
		for _, member := range candidate.Members {
			for _, log := range cfg.Logs {
				if log.DockerContainer != member {
					continue
				}
				if len(log.Masks) > 0 {
					return fmt.Errorf("cannot migrate %s: [log %s] has custom mask rules; configuration was not changed", candidate.Label(), log.Name)
				}
				if protected[log.Name] {
					return fmt.Errorf("cannot migrate %s: [log %s] has individual settings or comments; configuration was not changed", candidate.Label(), log.Name)
				}
			}
		}
	}
	// A project that is added replaces its containers registered one by one,
	// so that the same log is not read twice (spec 12.7.4).
	var removed []string
	for _, c := range chosen {
		for _, member := range c.Members {
			for _, log := range cfg.Logs {
				if log.DockerContainer == member {
					removed = append(removed, log.Name)
					fmt.Fprintf(out, "  [log %s] docker:%s is now read through %s and is removed.\n", log.Name, member, c.Label())
				}
			}
		}
	}
	if len(gone) > 0 {
		verb := "have"
		if len(gone) == 1 {
			verb = "has"
		}
		fmt.Fprintf(out, "%s in the configuration %s nothing to read any more:\n", count(len(gone), "log"), verb)
		labels := make([]string, len(gone))
		for i, g := range gone {
			labels[i] = fmt.Sprintf("[log %s] %s: %s", g.log.Name, g.log.Label(), g.reason)
		}
		for _, i := range askEach(reader, out, labels, "Remove it from the configuration?") {
			removed = append(removed, gone[i].log.Name)
		}
	}
	groups := map[string][]string{}
	if missing := groupless(cfg, removed, inventory); len(missing) > 0 {
		fmt.Fprintf(out, "%s in the configuration %s no group:\n", count(len(missing), "log"), map[bool]string{true: "has", false: "have"}[len(missing) == 1])
		labels := make([]string, len(missing))
		for i, m := range missing {
			labels[i] = fmt.Sprintf("[log %s] %s", m.log.Name, m.log.Label())
		}
		indices := askEachWith(reader, out, labels, func(i int) string { return fmt.Sprintf("Add group %q?", missing[i].group) })
		var candidates []Candidate
		for _, i := range indices {
			candidates = append(candidates, Candidate{Name: missing[i].log.Name, Path: missing[i].log.Path, DockerContainer: missing[i].log.DockerContainer, DockerProject: missing[i].log.DockerProject, JournalUnit: missing[i].log.JournalUnit, Group: missing[i].group})
		}
		if err := chooseCandidateGroups(reader, out, candidates); err != nil {
			return err
		}
		for index, candidate := range candidates {
			values := candidate.Groups
			if len(values) == 0 {
				values = []string{missing[indices[index]].group}
			}
			groups[candidate.Name] = values
		}
	}
	if manualAvailable {
		manual, err := chooseManualPaths(reader, out, opts, cfg.Logs, chosen)
		if err != nil {
			return err
		}
		chosen = append(chosen, manual...)
	}

	var unknownFormats []*Candidate
	preview, previewError := flattenCandidates(chosen, nil)
	if previewError != nil {
		return previewError
	}
	if err := validateCandidates(preview, opts.Root); err != nil {
		return err
	}
	for i := range chosen {
		if chosen[i].TimeFormat == "" {
			unknownFormats = append(unknownFormats, &chosen[i])
		}
	}
	if err := detectWithJev(reader, out, unknownFormats, opts); err != nil {
		return err
	}

	if err := chooseCandidateGroups(reader, out, chosen); err != nil {
		return err
	}
	explicitlyRemoved := chooseRegisteredRemovals(reader, out, cfg, removed)
	removed = append(removed, explicitlyRemoved...)
	if len(chosen) == 0 && len(removed) == 0 && len(groups) == 0 && len(changes.Paths) == 0 && len(changes.Projects) == 0 && len(changes.Formats) == 0 {
		if err := ensureRegisteredSample(opts.ConfigPath, out); err != nil {
			return err
		}
		fmt.Fprintf(out, "No registered log changes were selected in %s.\n", opts.ConfigPath)
		return nil
	}

	var kept []config.Log
	drop := map[string]bool{}
	for _, name := range removed {
		drop[name] = true
	}
	for _, entry := range cfg.Logs {
		if !drop[entry.Name] {
			kept = append(kept, entry)
		}
	}
	chosen, err = flattenCandidates(chosen, kept)
	if err != nil {
		return err
	}
	pruneRemovedChanges(removed, groups, changes)
	if err := saveRegisteredUpdate(opts.ConfigPath, removed, groups, chosen, changes, opts.Root); err != nil {
		return err
	}
	if len(chosen) > 0 {
		fmt.Fprintf(out, "Added %s to %s.\n", count(len(chosen), "log"), opts.ConfigPath)
	}
	if len(removed) > 0 {
		fmt.Fprintf(out, "Removed %s from %s.\n", count(len(removed), "log"), opts.ConfigPath)
	}
	if len(groups) > 0 {
		fmt.Fprintf(out, "Added a group to %s in %s.\n", count(len(groups), "log"), opts.ConfigPath)
	}
	if len(changes.Paths)+len(changes.Projects) > 0 {
		fmt.Fprintf(out, "Updated registered Docker paths in %s.\n", opts.ConfigPath)
	}
	if len(changes.Formats) > 0 {
		fmt.Fprintf(out, "Wrote explicit Docker time_format settings in %s.\n", opts.ConfigPath)
	}
	deleteRemovedHostLogs(reader, out, cfg, explicitlyRemoved, inventory, discoveryError, opts)
	for _, c := range chosen {
		if c.TimeFormat == "" {
			fmt.Fprintf(out, "  %s has no time_format yet and is skipped until it is set.\n", c.Label())
		}
	}
	return nil
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// askEach asks question about each label with [y/N/all] and returns the
// indexes answered yes. "all" says yes to that one and the rest; the end of
// the input says no to what is left.
func askEach(reader *bufio.Reader, out io.Writer, labels []string, question string) []int {
	return askEachWith(reader, out, labels, func(int) string { return question })
}

// askEachWith is askEach with a question per item.
func askEachWith(reader *bufio.Reader, out io.Writer, labels []string, question func(int) string) []int {
	var yes []int
	all := false
	for i, label := range labels {
		if all {
			yes = append(yes, i)
			fmt.Fprintf(out, "  %d/%d. %s: yes\n", i+1, len(labels), label)
			continue
		}
		answer, ok := ask(reader, out, fmt.Sprintf("  %d/%d. %s\n      %s [y/N/all] ", i+1, len(labels), label, question(i)))
		if !ok {
			break
		}
		switch answer {
		case "y", "yes":
			yes = append(yes, i)
		case "all", "a":
			yes = append(yes, i)
			all = true
		}
	}
	return yes
}

type grouplessLog struct {
	log   config.Log
	group string
}

// groupless returns the logs without a group, other than those about to be
// removed, with their default group (spec 12.7.4).
func groupless(cfg *config.Config, removed []string, inventory []docker.Container) []grouplessLog {
	skip := map[string]bool{}
	for _, name := range removed {
		skip[name] = true
	}
	var out []grouplessLog
	for _, log := range cfg.Logs {
		if len(log.Groups) > 0 || skip[log.Name] {
			continue
		}
		group := SystemGroup
		if log.DockerProject != "" {
			group = log.DockerProject
		} else if log.DockerContainer != "" {
			group = log.DockerContainer
			for _, container := range inventory {
				if container.Name == log.DockerContainer && config.GroupName.MatchString(container.Project) {
					group = container.Project
					break
				}
			}
		}
		out = append(out, grouplessLog{log: log, group: group})
	}
	return out
}

type goneLog struct {
	log    config.Log
	reason string
}

// missingLogs returns the configured logs whose source is no longer there.
func missingLogs(cfg *config.Config, root string) []goneLog {
	var gone []goneLog
	for _, log := range cfg.Logs {
		if reason := missingReason(log, root); reason != "" {
			gone = append(gone, goneLog{log: log, reason: reason})
		}
	}
	return gone
}

func missingReason(log config.Log, root string) string {
	switch {
	case log.DockerContainer != "" || log.DockerProject != "":
		if log.Path == "" {
			return ""
		}
		if err := docker.ValidateLogPath(root, log.Path); err != nil {
			return err.Error()
		}
	case log.JournalUnit != "":
		if _, err := journal.Find(root); err != nil {
			return err.Error()
		}
	case logread.IsPattern(log.Path):
		if matches, _ := filepath.Glob(filepath.Join(root, log.Path)); len(matches) == 0 {
			return "no file matches it"
		}
	default:
		if _, err := os.Stat(filepath.Join(root, log.Path)); errors.Is(err, os.ErrNotExist) {
			return "the file does not exist"
		}
	}
	return ""
}

// ask prints prompt and returns the lowercased answer; ok is false when the
// input ended without one.
func ask(reader *bufio.Reader, out io.Writer, prompt string) (string, bool) {
	fmt.Fprint(out, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(line)), true
}

// newCandidates drops what the configuration already reads and renames the
// rest away from the section names already in use.
func newCandidates(found []Candidate, cfg *config.Config) []Candidate {
	have := map[string]bool{}
	names := map[string]bool{}
	for _, log := range cfg.Logs {
		have[log.Label()] = true
		names[log.Name] = true
	}
	var fresh []Candidate
	for _, c := range found {
		if c.DockerProject != "" && c.DockerContainer == "" {
			registered := false
			legacy := false
			var sharedGroups []string
			commonGroups := true
			covered := map[string]bool{}
			for _, entry := range cfg.Logs {
				if entry.DockerProject == c.DockerProject {
					registered = true
					if sharedGroups == nil {
						sharedGroups = append([]string{}, entry.Groups...)
					} else if strings.Join(sharedGroups, "\x00") != strings.Join(entry.Groups, "\x00") {
						commonGroups = false
					}
					if entry.DockerContainer == "" {
						legacy = true
					}
				}
				if entry.DockerContainer != "" {
					covered[entry.DockerContainer] = true
				}
			}
			if legacy {
				continue
			}
			if registered {
				if commonGroups && len(sharedGroups) > 0 {
					c.Groups = sharedGroups
					c.Group = ""
				}
				var members []containers.Member
				var names []string
				for _, member := range c.MemberLogs {
					if !covered[member.Name] {
						members = append(members, member)
						names = append(names, member.Name)
					}
				}
				c.MemberLogs, c.Members = members, names
				if len(members) == 0 {
					continue
				}
			}
		}
		if have[c.Label()] {
			continue
		}
		base, name := c.Name, c.Name
		for n := 2; names[name]; n++ {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		names[name] = true
		c.Name = name
		fresh = append(fresh, c)
	}
	return fresh
}

// migrationSettings inspects explicit settings rather than resolved defaults,
// because moving a member's policy to the project would affect other members.
func migrationSettings(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	protected := map[string]bool{}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		content, comment, err := config.SplitComment(line)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(content)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = ""
			fields := strings.Fields(trimmed[1 : len(trimmed)-1])
			if len(fields) == 2 && fields[0] == "log" {
				section = fields[1]
				if comment != "" {
					protected[section] = true
				}
			}
			continue
		}
		if section == "" {
			continue
		}
		if comment != "" {
			protected[section] = true
		}
		if trimmed == "" {
			continue
		}
		key, _, assignment := strings.Cut(trimmed, "=")
		if !assignment || strings.TrimSpace(key) != "docker_container" {
			protected[section] = true
		}
	}
	return protected, nil
}
