// Package containers holds everything jevtri does with container engines
// (spec 12.7.1, 12.7.4): which containers to offer for registration, how a
// registered Compose project becomes one log per container at each run, what
// is running but not registered, and the default group of a container.
//
// setup (init, --config-update) and the run itself call only this package, so
// that container handling can change, or another engine such as Podman can be
// added, without touching them. Docker is the only engine for now.
package containers

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/docker"
)

// Offer is something that can be registered: a whole Compose project, or one
// container that Compose did not start.
type Offer struct {
	Project    string   // set for a Compose project
	Container  string   // set for a single container
	Members    []string // container names of a project
	Group      string   // default group
	Path       string
	MemberLogs []Member
}

type Member struct {
	Name string
	Path string
}

func HostPath(root, path string) string {
	if root == "" {
		return path
	}
	relative, err := filepath.Rel(root, path)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return string(filepath.Separator) + relative
	}
	return path
}

func Discover(root string) ([]Offer, error) {
	list, err := docker.List(root)
	if err != nil {
		return nil, err
	}
	return FromInventory(list, root), nil
}

// Label is how the offer is shown and how its logs are named when sent.
func (o Offer) Label() string {
	if o.Project != "" {
		return "compose:" + o.Project
	}
	return "docker:" + o.Container
}

// Offers returns the Compose projects and the other containers that have a
// json-file log, projects first in the order Docker lists them by name.
// Without Docker (or without permission) there is nothing to offer.
func Offers(root string) []Offer {
	list, err := docker.List(root)
	if err != nil {
		return nil
	}
	return FromInventory(list, root)
}

func FromInventory(list []docker.Container, root string) []Offer {
	var offers []Offer
	index := map[string]int{}
	for _, c := range list {
		if c.LogPath == "" || !config.GroupName.MatchString(c.Name) {
			continue
		}
		if project := projectOf(c); project != "" {
			i, ok := index[project]
			if !ok {
				i = len(offers)
				index[project] = i
				offers = append(offers, Offer{Project: project, Group: project})
			}
			offers[i].Members = append(offers[i].Members, c.Name)
			offers[i].MemberLogs = append(offers[i].MemberLogs, Member{Name: c.Name, Path: HostPath(root, c.LogPath)})
			continue
		}
		offers = append(offers, Offer{Container: c.Name, Group: c.Name, Path: HostPath(root, c.LogPath)})
	}
	return offers
}

// DefaultGroup is the group a container is in when the configuration does
// not say: its Compose project, or its own name. Unknown containers get "".
func DefaultGroup(root, container string) string {
	list, err := docker.List(root)
	if err != nil {
		return ""
	}
	for _, c := range list {
		if c.Name == container {
			if project := projectOf(c); project != "" {
				return project
			}
			return c.Name
		}
	}
	return ""
}

// projectOf returns a usable Compose project name of c, or "".
func projectOf(c docker.Container) string {
	if c.Project != "" && config.GroupName.MatchString(c.Project) {
		return c.Project
	}
	return ""
}

// Expand replaces each docker_project log with one docker_container log per
// container of the project found now. A project with no readable container is
// kept as it is, so that reading it reports why. notes say what was not read.
func Expand(logs []config.Log, root string) (expanded []config.Log, notes []string) {
	for _, entry := range logs {
		if entry.DockerProject == "" {
			expanded = append(expanded, entry)
			continue
		}
		readable, skipped, err := docker.Project(root, entry.DockerProject)
		if err != nil || len(readable) == 0 {
			expanded = append(expanded, entry)
			continue
		}
		for _, c := range skipped {
			notes = append(notes, fmt.Sprintf("compose:%s: container %s is not read: %v (driver %s)", entry.DockerProject, c.Name, docker.ErrNoFile, c.Driver))
		}
		for _, c := range readable {
			member := entry
			member.Name = entry.Name + "/" + c.Name
			member.DockerProject, member.DockerContainer = "", c.Name
			expanded = append(expanded, member)
		}
	}
	return expanded, notes
}

// ValidateNames must run after expansion, before names become lookup keys.
func ValidateNames(logs []config.Log) error {
	seen := map[string]bool{}
	for _, entry := range logs {
		if seen[entry.Name] {
			return fmt.Errorf("duplicate log name after container expansion: %q", entry.Name)
		}
		seen[entry.Name] = true
	}
	return nil
}

// ProjectError explains why a docker_project log could not be expanded.
func ProjectError(root, project string) error {
	readable, _, err := docker.Project(root, project)
	if err != nil {
		return err
	}
	if len(readable) == 0 {
		return fmt.Errorf("no container of this Docker Compose project has a json-file log")
	}
	return nil
}

// Uncovered returns the Compose projects and the single containers with a
// json-file log that none of logs reads.
func Uncovered(logs []config.Log, root string) (projects, single []string) {
	list, err := docker.List(root)
	if err != nil {
		return nil, nil
	}
	covered, registered := map[string]bool{}, map[string]bool{}
	for _, entry := range logs {
		covered[entry.DockerContainer] = true
		registered[entry.DockerProject] = true
	}
	seen := map[string]bool{}
	for _, c := range list {
		project := projectOf(c)
		switch {
		case c.LogPath == "" || covered[c.Name] || (project != "" && registered[project]):
		case project != "":
			if !seen[project] {
				seen[project] = true
				projects = append(projects, project)
			}
		default:
			single = append(single, c.Name)
		}
	}
	return projects, single
}

// LogFile returns the file to read for a single registered container.
func LogFile(root, container string) (string, error) {
	return docker.LogFile(root, container)
}

// Missing explains why a registered container or project has nothing to read
// any more, or returns "" when it is there.
func Missing(log config.Log, root string) string {
	switch {
	case log.DockerContainer != "":
		if _, err := docker.LogFile(root, log.DockerContainer); err != nil {
			return err.Error()
		}
	case log.DockerProject != "":
		if err := ProjectError(root, log.DockerProject); err != nil {
			return err.Error()
		}
	}
	return ""
}
