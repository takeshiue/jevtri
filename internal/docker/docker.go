// Package docker detects container log paths only when configuration is registered
// or updated. Production discovery uses Docker's reported paths, not data-root guesses.
package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/takeshiue/jevtri/internal/safeopen"
)

// ContainersDir describes the legacy metadata layout used only by isolated fixtures.
const ContainersDir = "/var/lib/docker/containers"

// Container is what jevtri needs to know about one container.
type Container struct {
	Name     string // without Docker's leading "/"
	ID       string
	LogPath  string // empty when the reported file cannot be safely read
	LogError error  // the reason the reported path was rejected
	Driver   string // log driver, such as json-file or journald
	Running  bool
	// Project is the Docker Compose project (label com.docker.compose.project),
	// empty for a container not started by Compose.
	Project string
	Image   string
}

// ErrNotFound means no container has the name.
var ErrNotFound = errors.New("no Docker container has this name")

// ErrNoFile means the container has no json-file log inside its own directory
// (another log driver, or metadata pointing elsewhere).
var ErrNoFile = errors.New("the container has no json-file log jevtri can read")

// maxMetadata caps config.v2.json and hostconfig.json; they are a few KB.
const maxMetadata = 4 << 20

// List detects every container, including stopped ones, for registration/update.
// A nonempty root selects the isolated legacy metadata fixture, never production.
func List(root string) ([]Container, error) {
	if root == "" {
		return listCLI()
	}
	dir := filepath.Join(root, ContainersDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var containers []Container
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		c, err := read(root, filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		containers = append(containers, c)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}

// LogFile returns the path of the json-file log of the container called name.
func LogFile(root, name string) (string, error) {
	containers, err := List(root)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", filepath.Join(root, ContainersDir), err)
	}
	for _, c := range containers {
		if c.Name != name {
			continue
		}
		if c.LogPath == "" {
			if c.LogError != nil {
				return "", fmt.Errorf("%w: %v", ErrNoFile, c.LogError)
			}
			return "", fmt.Errorf("%w (driver %s)", ErrNoFile, c.Driver)
		}
		return c.LogPath, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, name)
}

func read(root, dir string) (Container, error) {
	var config struct {
		ID      string
		Name    string
		LogPath string
		State   struct{ Running bool }
		Config  struct {
			Image  string
			Labels map[string]string
		}
	}
	if err := readJSON(filepath.Join(dir, "config.v2.json"), &config); err != nil {
		return Container{}, err
	}
	var host struct {
		LogConfig struct{ Type string }
	}
	// Older or partial directories may lack hostconfig.json; LogPath decides then.
	_ = readJSON(filepath.Join(dir, "hostconfig.json"), &host)

	c := Container{
		Name:    strings.TrimPrefix(config.Name, "/"),
		ID:      config.ID,
		Driver:  host.LogConfig.Type,
		Running: config.State.Running,
		Project: config.Config.Labels["com.docker.compose.project"],
		Image:   config.Config.Image,
	}
	// A name that could not be written into the configuration is skipped.
	if c.Name == "" || strings.ContainsFunc(c.Name, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '/' }) {
		return Container{}, fmt.Errorf("unusable container name %q", config.Name)
	}
	if c.Driver == "" && config.LogPath != "" {
		c.Driver = "json-file"
	}
	if c.Driver != "json-file" {
		return c, nil
	}
	logPath := config.LogPath
	if logPath == "" && config.ID != "" {
		logPath = filepath.Join(strings.TrimPrefix(dir, root), config.ID+"-json.log")
	}
	// The log must lie in the container's own directory: jevtri reads it as
	// root and sends it, so metadata must not point it anywhere else.
	own := filepath.Join(strings.TrimPrefix(dir, root)) + string(filepath.Separator)
	if logPath != "" && filepath.IsAbs(logPath) && strings.HasPrefix(filepath.Clean(logPath), own) {
		c.LogPath = filepath.Join(root, filepath.Clean(logPath))
	}
	return c, nil
}

func readJSON(path string, value any) error {
	handle, err := safeopen.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxMetadata))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

// Project returns the containers of a Docker Compose project that have a
// json-file log jevtri can read, sorted by name. Containers of the project
// with another log driver are returned in skipped.
func Project(root, project string) (readable, skipped []Container, err error) {
	containers, err := List(root)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read %s: %v", filepath.Join(root, ContainersDir), err)
	}
	for _, c := range containers {
		if c.Project != project {
			continue
		}
		if c.LogPath == "" {
			skipped = append(skipped, c)
			continue
		}
		readable = append(readable, c)
	}
	return readable, skipped, nil
}
