package setup

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/docker"
	"github.com/takeshiue/jevtri/internal/logread"
	"github.com/takeshiue/jevtri/internal/printsafe"
	"github.com/takeshiue/jevtri/internal/safeopen"
)

func showInventoryIssues(out io.Writer, inventory []docker.Container) {
	for _, container := range inventory {
		if container.LogError != nil {
			fmt.Fprintf(out, "%s\n", printsafe.Line(fmt.Sprintf("Docker container %q was not offered: %v. Existing registrations are kept; register an absolute log path manually.", container.Name, container.LogError)))
		}
	}
}

func chooseManualPaths(reader *bufio.Reader, out io.Writer, opts Options, existing []config.Log, selected []Candidate) ([]Candidate, error) {
	fmt.Fprintln(out, "Other absolute log paths to register manually, one per line (Enter finishes):")
	names := map[string]bool{}
	for _, entry := range existing {
		names[entry.Name] = true
	}
	for _, candidate := range selected {
		names[candidate.Name] = true
	}
	var added []Candidate
	for {
		fmt.Fprint(out, "Path> ")
		line, err := reader.ReadString('\n')
		path := strings.TrimSpace(line)
		if path == "" {
			break
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err := docker.ValidateLogPath(opts.Root, path); err != nil {
			return nil, err
		}
		name := stem(path)
		base := name
		for suffix := 2; names[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		names[name] = true
		format, assumed := detectFormat(filepath.Join(opts.Root, path), []string{"docker-json"})
		added = append(added, Candidate{Name: name, Path: path, TimeFormat: format, Assumed: assumed, Group: SystemGroup})
		if err != nil {
			break
		}
	}
	return added, nil
}

func chooseCandidateGroups(reader *bufio.Reader, out io.Writer, candidates []Candidate) error {
	if len(candidates) > 1 {
		for _, candidate := range candidates {
			defaults := candidate.Groups
			if len(defaults) == 0 && candidate.Group != "" {
				defaults = []string{candidate.Group}
			}
			fmt.Fprintf(out, "  %s: %s\n", candidate.Label(), strings.Join(defaults, ", "))
		}
		answer, ok := ask(reader, out, "Use these groups for all logs? [y/N] ")
		if ok && (answer == "y" || answer == "yes") {
			return nil
		}
	}
	for index := range candidates {
		candidate := &candidates[index]
		defaults := candidate.Groups
		if len(defaults) == 0 && candidate.Group != "" {
			defaults = []string{candidate.Group}
		}
		fmt.Fprintf(out, "Groups for %s: %s. Enter keeps these groups; otherwise enter one group per line and finish with an empty line.\n", candidate.Label(), strings.Join(defaults, ", "))
		var chosen []string
		seen := map[string]bool{}
		for {
			fmt.Fprint(out, "Group> ")
			line, err := reader.ReadString('\n')
			group := strings.TrimSpace(line)
			if group == "" {
				break
			}
			if !config.GroupName.MatchString(group) {
				return fmt.Errorf("invalid group name; configuration was not changed")
			}
			if seen[group] {
				return fmt.Errorf("duplicate group; configuration was not changed")
			}
			seen[group] = true
			chosen = append(chosen, group)
			if err != nil {
				break
			}
		}
		if len(chosen) > 0 {
			candidate.Groups = chosen
			candidate.Group = ""
		}
	}
	return nil
}

func flattenCandidates(candidates []Candidate, existing []config.Log) ([]Candidate, error) {
	names := map[string]bool{}
	containers := map[string]bool{}
	for _, entry := range existing {
		names[entry.Name] = true
		if entry.DockerContainer != "" {
			containers[entry.DockerContainer] = true
		}
	}
	var flat []Candidate
	for _, candidate := range candidates {
		members := []Candidate{candidate}
		if candidate.DockerProject != "" && candidate.DockerContainer == "" {
			if len(candidate.MemberLogs) == 0 {
				return nil, fmt.Errorf("no readable log paths for %s; register absolute paths manually", candidate.Label())
			}
			members = nil
			for _, member := range candidate.MemberLogs {
				child := candidate
				child.Name, child.Path, child.DockerContainer = candidate.Name+"-"+member.Name, member.Path, member.Name
				child.Members, child.MemberLogs = nil, nil
				members = append(members, child)
			}
		}
		for _, member := range members {
			if member.DockerContainer != "" {
				if containers[member.DockerContainer] {
					return nil, fmt.Errorf("container %s is already registered; configuration was not changed", member.DockerContainer)
				}
				containers[member.DockerContainer] = true
			}
			base := member.Name
			for suffix := 2; names[member.Name]; suffix++ {
				member.Name = fmt.Sprintf("%s-%d", base, suffix)
			}
			names[member.Name] = true
			flat = append(flat, member)
		}
	}
	return flat, nil
}

func validateCandidates(candidates []Candidate, root string) error {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		for _, value := range []string{candidate.Path, candidate.TimeFormat, candidate.DockerContainer, candidate.DockerProject, candidate.JournalUnit, candidate.Group} {
			if _, err := config.FormatValue(value); err != nil {
				return err
			}
		}
		for _, group := range candidate.Groups {
			if _, err := config.FormatValue(group); err != nil {
				return err
			}
		}
		if candidate.JournalUnit != "" {
			continue
		}
		if !filepath.IsAbs(candidate.Path) {
			return fmt.Errorf("log path must be absolute; configuration was not changed")
		}
		if strings.ContainsFunc(candidate.Path, func(character rune) bool { return character < 0x20 || character == 0x7f }) {
			return fmt.Errorf("log path contains a control character; configuration was not changed")
		}
		if seen[candidate.Path] {
			return fmt.Errorf("duplicate log path; configuration was not changed")
		}
		seen[candidate.Path] = true
		if candidate.DockerContainer != "" || candidate.DockerProject != "" {
			if err := docker.ValidateLogPath(root, candidate.Path); err != nil {
				return fmt.Errorf("cannot register %s: %w", candidate.Label(), err)
			}
			continue
		}
		if logread.IsPattern(candidate.Path) {
			if len(readableMatches(root, candidate.Path)) == 0 {
				return fmt.Errorf("no readable files for %s", candidate.Path)
			}
			continue
		}
		handle, err := safeopen.Open(filepath.Join(root, candidate.Path))
		if err != nil {
			return err
		}
		if err := handle.Close(); err != nil {
			return err
		}
	}
	return nil
}
