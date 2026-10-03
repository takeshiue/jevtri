package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/takeshiue/jevtri/internal/safeopen"
)

// Paths exclude user-writable PATH entries when discovery runs as root.
var Paths = []string{"/usr/bin/docker", "/bin/docker", "/usr/local/bin/docker"}

// Timeout bounds the complete inventory, not each individual container.
var Timeout = 30 * time.Second

const maxCLIOutput = 8 << 20
const inspectTemplate = `{"id":{{json .Id}},"name":{{json .Name}},"log_path":{{json .LogPath}},"driver":{{json .HostConfig.LogConfig.Type}},"running":{{json .State.Running}},"project":{{json (index .Config.Labels "com.docker.compose.project")}}}`

var contextName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var inventoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ErrNoCLI is distinct from an inventory containing no containers.
var ErrNoCLI = errors.New("docker CLI was not found; specify the absolute log path manually")

// ValidateLogPath uses the same safe regular-file open for detected and manual paths.
func ValidateLogPath(root, path string) error {
	if !validAbsolutePath(path) {
		return errors.New("log path must be an absolute path without control characters or glob characters or parent traversal")
	}
	actual := path
	if root != "" && !within(root, path) {
		actual = filepath.Join(root, path)
	}
	handle, err := safeopen.Open(actual)
	if err != nil {
		return fmt.Errorf("cannot read the configured log path: %w", err)
	}
	return handle.Close()
}

func validAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "*?[]") && !strings.ContainsFunc(path, unicode.IsControl)
}

func within(directory, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

type cliInventory struct {
	executable  string
	environment []string
	remaining   int64
}

var checkCLIExecutable = validateCLIExecutable

func validateCLIExecutable(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return errors.New("cannot resolve Docker CLI")
	}
	if !filepath.IsAbs(path) {
		return errors.New("docker CLI path must be absolute")
	}
	for _, start := range []string{filepath.Dir(path), resolved} {
		for current := start; ; current = filepath.Dir(current) {
			information, err := os.Stat(current)
			if err != nil {
				return errors.New("cannot inspect Docker CLI ownership")
			}
			metadata, ok := information.Sys().(*syscall.Stat_t)
			if !ok || metadata.Uid != 0 || information.Mode().Perm()&0o022 != 0 {
				return errors.New("docker CLI or its directory is writable by an untrusted user")
			}
			if current == string(filepath.Separator) {
				break
			}
		}
	}
	return nil
}

func findCLI() (string, error) {
	for _, path := range Paths {
		information, err := os.Stat(path)
		if err == nil && information.Mode().IsRegular() && information.Mode().Perm()&0o111 != 0 {
			if err := checkCLIExecutable(path); err != nil {
				return "", err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", errors.New("cannot resolve Docker CLI")
			}
			return resolved, nil
		}
	}
	return "", ErrNoCLI
}

func discoveryEnvironment() []string {
	environment := []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "NO_COLOR=1"}
	for _, key := range []string{"HOME", "DOCKER_CONFIG"} {
		if value, present := os.LookupEnv(key); present {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func (inventory *cliInventory) run(ctx context.Context, arguments ...string) ([]byte, error) {
	if inventory.remaining <= 0 {
		return nil, errors.New("docker discovery output exceeds 8 MiB")
	}
	command := exec.CommandContext(ctx, inventory.executable, arguments...)
	command.Env = inventory.environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 2 * time.Second
	// Docker errors and complete inspect data can contain user secrets.
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("cannot create Docker discovery output pipe")
	}
	if err := command.Start(); err != nil {
		return nil, errors.New("cannot start Docker CLI")
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, inventory.remaining+1))
	overflow := int64(len(output)) > inventory.remaining
	if overflow || readErr != nil {
		_ = command.Cancel()
	}
	waitErr := command.Wait()
	if overflow {
		return nil, errors.New("docker discovery output exceeds 8 MiB")
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("docker discovery did not finish within %s", Timeout)
	}
	if readErr != nil {
		return nil, errors.New("cannot read Docker discovery output")
	}
	if waitErr != nil {
		return nil, errors.New("docker CLI failed; verify its installation, local daemon and permissions, or specify the absolute log path manually")
	}
	inventory.remaining -= int64(len(output))
	return output, nil
}

func decodeString(output []byte) (string, error) {
	var value string
	if err := json.Unmarshal(output, &value); err != nil {
		return "", errors.New("docker discovery returned an invalid JSON string")
	}
	return value, nil
}

func (inventory *cliInventory) endpoint(ctx context.Context) (string, error) {
	selectedContext := os.Getenv("DOCKER_CONTEXT")
	endpoint := ""
	if selectedContext == "" {
		endpoint = os.Getenv("DOCKER_HOST")
	}
	if endpoint == "" {
		if selectedContext == "" {
			output, err := inventory.run(ctx, "context", "show")
			if err != nil {
				return "", err
			}
			selectedContext = strings.TrimSpace(string(output))
		}
		if !contextName.MatchString(selectedContext) {
			return "", errors.New("docker context name is invalid")
		}
		output, err := inventory.run(ctx, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}", selectedContext)
		if err != nil {
			return "", err
		}
		endpoint, err = decodeString(output)
		if err != nil {
			return "", err
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || parsed.Opaque != "" || !validAbsolutePath(parsed.Path) {
		return "", errors.New("docker discovery requires a local Unix socket; remote Docker endpoints are not supported")
	}
	return (&url.URL{Scheme: "unix", Path: parsed.Path}).String(), nil
}

type inspectedContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	LogPath string `json:"log_path"`
	Driver  string `json:"driver"`
	Running bool   `json:"running"`
	Project string `json:"project"`
}

func listCLI() ([]Container, error) {
	executable, err := findCLI()
	if err != nil {
		return nil, err
	}
	inventory := cliInventory{executable: executable, environment: discoveryEnvironment(), remaining: maxCLIOutput}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	endpoint, err := inventory.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	information, err := inventory.run(ctx, "--host", endpoint, "info", "--format", "{{json .DockerRootDir}}")
	if err != nil {
		return nil, err
	}
	dataRoot, err := decodeString(information)
	if err != nil {
		return nil, err
	}
	if !validAbsolutePath(dataRoot) {
		return nil, errors.New("docker returned an invalid data-root path")
	}
	output, err := inventory.run(ctx, "--host", endpoint, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	var containers []Container
	seen := map[string]bool{}
	seenNames := map[string]bool{}
	for _, id := range strings.Fields(string(output)) {
		if !containerID.MatchString(id) || seen[id] {
			return nil, errors.New("docker returned an invalid or repeated container ID")
		}
		seen[id] = true
		output, err := inventory.run(ctx, "--host", endpoint, "container", "inspect", "--format", inspectTemplate, id)
		if err != nil {
			return nil, fmt.Errorf("docker container inspection failed; inventory is incomplete: %w", err)
		}
		var inspected inspectedContainer
		decoder := json.NewDecoder(bytes.NewReader(output))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&inspected); err != nil {
			return nil, errors.New("docker returned invalid container metadata")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("docker returned trailing container metadata")
		}
		name := strings.TrimPrefix(inspected.Name, "/")
		if inspected.ID != id || !contextName.MatchString(name) || seenNames[name] || (inspected.Project != "" && !inventoryName.MatchString(inspected.Project)) || strings.ContainsFunc(inspected.Driver, unicode.IsControl) {
			return nil, errors.New("docker returned an invalid container identity")
		}
		seenNames[name] = true
		container := Container{ID: id, Name: name, Driver: inspected.Driver, Running: inspected.Running, Project: inspected.Project}
		switch {
		case inspected.Driver != "json-file":
			container.LogError = fmt.Errorf("docker logging driver %q has no supported json-file log; specify a readable text log path manually", inspected.Driver)
		case !validAbsolutePath(inspected.LogPath):
			container.LogError = errors.New("docker did not report a valid absolute log path; specify the log path manually")
		case !within(filepath.Join(dataRoot, "containers", id), inspected.LogPath):
			container.LogError = errors.New("docker reported a log outside its container directory; specify the log path manually")
		default:
			container.LogError = ValidateLogPath("", inspected.LogPath)
			if container.LogError == nil {
				container.LogPath = inspected.LogPath
			}
		}
		containers = append(containers, container)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}
