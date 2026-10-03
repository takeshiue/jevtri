package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/containers"
	"github.com/takeshiue/jevtri/internal/docker"
)

type dockerChanges struct {
	Paths    map[string]string
	Projects map[string][]Candidate
	Formats  map[string]string
}

func chooseDockerChanges(reader *bufio.Reader, out io.Writer, cfg *config.Config, inventory []docker.Container, opts Options) (dockerChanges, error) {
	changes := dockerChanges{Paths: map[string]string{}, Projects: map[string][]Candidate{}, Formats: map[string]string{}}
	explicit, err := explicitDockerFormats(opts.ConfigPath)
	if err != nil {
		return changes, err
	}
	all := false
	approve := func(prompt string) bool {
		if all {
			return true
		}
		answer, ok := ask(reader, out, prompt+" [y/N/all] ")
		if ok && answer == "all" {
			all = true
			return true
		}
		return ok && (answer == "y" || answer == "yes")
	}
	byName := map[string]docker.Container{}
	for _, container := range inventory {
		byName[container.Name] = container
	}
	offers := containers.FromInventory(inventory, opts.Root)
	for _, entry := range cfg.Logs {
		if entry.DockerContainer == "" && entry.DockerProject == "" {
			continue
		}
		if entry.DockerProject != "" && entry.DockerContainer == "" && entry.Path == "" {
			var offer *containers.Offer
			for index := range offers {
				if offers[index].Project == entry.DockerProject {
					offer = &offers[index]
					break
				}
			}
			if offer != nil {
				fmt.Fprintf(out, "Existing groups for %s: %s (kept).\n", entry.Label(), strings.Join(entry.Groups, ", "))
				if !approve(fmt.Sprintf("Register current absolute log paths for compose:%s?", entry.DockerProject)) {
					continue
				}
				for _, member := range offer.MemberLogs {
					for _, other := range cfg.Logs {
						if other.Name != entry.Name && other.DockerContainer == member.Name {
							return changes, fmt.Errorf("cannot migrate compose:%s: container %s has an individual registration; configuration was not changed", entry.DockerProject, member.Name)
						}
					}
				}
				candidate := Candidate{Name: entry.Name, DockerProject: entry.DockerProject, MemberLogs: offer.MemberLogs, Groups: entry.Groups, TimeFormat: entry.TimeFormat}
				members, err := flattenCandidates([]Candidate{candidate}, cfg.Logs)
				if err != nil {
					return changes, err
				}
				if err := validateCandidates(members, opts.Root); err != nil {
					return changes, err
				}
				changes.Projects[entry.Name] = members
				continue
			}
		}
		container, found := byName[entry.DockerContainer]
		path := ""
		if found && container.LogPath != "" {
			path = containers.HostPath(opts.Root, container.LogPath)
		}
		if path != "" {
			if err := docker.ValidateLogPath(opts.Root, path); err != nil {
				return changes, err
			}
			if path == entry.Path {
				if !explicit[entry.Name] && approve(fmt.Sprintf("Write time_format = %s explicitly for %s?", entry.TimeFormat, entry.Label())) {
					changes.Formats[entry.Name] = entry.TimeFormat
				}
				continue
			}
			fmt.Fprintf(out, "Existing groups for %s: %s (kept).\n", entry.Label(), strings.Join(entry.Groups, ", "))
			fmt.Fprintf(out, "Registered path for %s: %s\nDetected absolute path: %s\n", entry.Label(), entry.Path, path)
			if !approve("Update the registered path?") {
				continue
			}
		} else {
			if entry.Path != "" && docker.ValidateLogPath(opts.Root, entry.Path) == nil {
				if !explicit[entry.Name] && approve(fmt.Sprintf("Write time_format = %s explicitly for %s?", entry.TimeFormat, entry.Label())) {
					changes.Formats[entry.Name] = entry.TimeFormat
				}
				continue
			}
			fmt.Fprintf(out, "No readable path was detected for %s. Enter its absolute log path manually; Enter keeps the configuration unchanged.\nPath> ", entry.Label())
			line, err := reader.ReadString('\n')
			path = strings.TrimSpace(line)
			if path == "" {
				continue
			}
			if err != nil && err != io.EOF {
				return changes, err
			}
		}
		if err := docker.ValidateLogPath(opts.Root, path); err != nil {
			return changes, fmt.Errorf("cannot register path for %s: %w", entry.Label(), err)
		}
		changes.Paths[entry.Name] = path
		if !explicit[entry.Name] {
			changes.Formats[entry.Name] = entry.TimeFormat
		}
	}
	return changes, nil
}

func explicitDockerFormats(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, block := range splitConfigBlocks(string(data)) {
		for _, line := range block.Lines {
			content, err := config.StripComment(line)
			if err != nil {
				return nil, err
			}
			key, value, ok := strings.Cut(strings.TrimSpace(content), "=")
			if ok && strings.TrimSpace(key) == "time_format" {
				value = strings.TrimSpace(value)
				result[block.Name] = value != "" && value != "\"\"" && value != "''"
			}
		}
	}
	return result, nil
}
