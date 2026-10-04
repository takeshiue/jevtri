package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeCLIConfig struct {
	Host              string
	DataRoot          string
	Containers        []inspectedContainer
	IDs               string
	Failure           string
	OutputBytes       int
	OutputCommand     string
	DelayCommand      string
	DelayMilliseconds int
	SpawnChild        bool
	InspectOutput     string
	FailureID         string
}

type cliCall struct {
	Arguments   []string
	Environment []string
}

func TestDockerCLIHelper(t *testing.T) {
	marker := -1
	for index, argument := range os.Args {
		if argument == "--" {
			marker = index
			break
		}
	}
	if marker < 0 {
		return
	}
	configurationPath := os.Args[marker+1]
	arguments := os.Args[marker+2:]
	raw, err := os.ReadFile(configurationPath)
	if err != nil {
		os.Exit(91)
	}
	var configuration fakeCLIConfig
	if json.Unmarshal(raw, &configuration) != nil {
		os.Exit(92)
	}
	record, _ := json.Marshal(cliCall{Arguments: arguments, Environment: os.Environ()})
	file, err := os.OpenFile(configurationPath+".calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(93)
	}
	file.Write(append(record, '\n'))
	file.Close()
	command := ""
	if len(arguments) > 1 && arguments[0] == "--host" {
		if arguments[1] != configuration.Host {
			fmt.Fprintln(os.Stderr, "secret endpoint error")
			os.Exit(94)
		}
		arguments = arguments[2:]
	}
	switch {
	case len(arguments) >= 2 && arguments[0] == "context":
		command = "context-" + arguments[1]
	case len(arguments) >= 1 && arguments[0] == "info":
		command = "info"
	case len(arguments) >= 2 && arguments[0] == "container":
		command = arguments[1]
	}
	if configuration.Failure == command || (command == "inspect" && configuration.FailureID != "" && arguments[len(arguments)-1] == configuration.FailureID) {
		fmt.Fprintln(os.Stderr, "secret stderr must never escape")
		os.Exit(95)
	}
	if configuration.DelayCommand == command {
		if configuration.SpawnChild {
			child := exec.Command("/bin/sleep", "60")
			if child.Start() != nil {
				os.Exit(96)
			}
			os.WriteFile(configurationPath+".child", []byte(strconv.Itoa(child.Process.Pid)), 0600)
		}
		time.Sleep(time.Duration(configuration.DelayMilliseconds) * time.Millisecond)
	}
	if configuration.OutputCommand == command && configuration.OutputBytes > 0 {
		os.Stdout.Write([]byte(strings.Repeat("x", configuration.OutputBytes)))
		os.Exit(0)
	}
	switch command {
	case "context-show":
		fmt.Println("rootless")
	case "context-inspect":
		json.NewEncoder(os.Stdout).Encode(configuration.Host)
	case "info":
		json.NewEncoder(os.Stdout).Encode(configuration.DataRoot)
	case "ls":
		if configuration.IDs != "" {
			fmt.Println(configuration.IDs)
		} else {
			for _, container := range configuration.Containers {
				fmt.Println(container.ID)
			}
		}
	case "inspect":
		identity := arguments[len(arguments)-1]
		for _, container := range configuration.Containers {
			if container.ID == identity {
				if configuration.InspectOutput != "" {
					fmt.Print(configuration.InspectOutput)
				} else {
					json.NewEncoder(os.Stdout).Encode(container)
				}
				os.Exit(0)
			}
		}
		os.Exit(97)
	default:
		os.Exit(98)
	}
	os.Exit(0)
}

func fakeCLI(t *testing.T, configuration fakeCLIConfig) string {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "fixture.json")
	encoded, _ := json.Marshal(configuration)
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	executable := filepath.Join(directory, "docker")
	script := "#!/bin/sh\nexec " + quote(os.Args[0]) + " -test.run=^TestDockerCLIHelper$ -- " + quote(configPath) + " \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	previous := Paths
	previousCheck := checkCLIExecutable
	checkCLIExecutable = func(string) error { return nil }
	t.Cleanup(func() { checkCLIExecutable = previousCheck })
	Paths = []string{executable}
	t.Cleanup(func() { Paths = previous })
	t.Setenv("HOME", directory)
	t.Setenv("DOCKER_CONFIG", directory)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", "")
	return configPath
}

func calls(t *testing.T, path string) []cliCall {
	t.Helper()
	raw, err := os.ReadFile(path + ".calls")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var result []cliCall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call cliCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		result = append(result, call)
	}
	return result
}

func detectedContainer(t *testing.T, directory, name string) inspectedContainer {
	t.Helper()
	identity := strings.Repeat("a", 64)
	path := filepath.Join(directory, "containers", identity, "actual-json.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return inspectedContainer{ID: identity, Name: "/" + name, LogPath: path, Driver: "json-file", Project: "shop", Running: false}
}

func TestCLIInventoryDetectedPaths(t *testing.T) {
	for _, directoryName := range []string{"custom-data-root", "home/.local/share/arbitrary-root"} {
		t.Run(directoryName, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), directoryName)
			container := detectedContainer(t, directory, "web")
			configuration := fakeCLIConfig{Host: "unix:///run/example.sock", DataRoot: directory, Containers: []inspectedContainer{container}}
			fakeCLI(t, configuration)
			inventory, err := List("")
			if err != nil || len(inventory) != 1 {
				t.Fatalf("inventory=%+v err=%v", inventory, err)
			}
			if inventory[0].LogPath != container.LogPath || inventory[0].Running || inventory[0].LogError != nil {
				t.Fatalf("wrong discovered stopped/empty log: %+v", inventory[0])
			}
		})
	}
}

func TestCLIEndpointSelection(t *testing.T) {
	for _, test := range []struct {
		name, context, host, reported string
		daemon                        bool
		contextCalls                  int
	}{
		{"default", "", "", "unix:///run/default.sock", true, 2},
		{"host", "", "unix:///run/explicit.sock", "unix:///run/explicit.sock", true, 0},
		{"context overrides remote host", "rootless", "ssh://remote.example", "unix:///run/user/123/socket", true, 1},
		{"remote context rejected", "rootless", "unix:///run/ignored.sock", "ssh://remote.example", false, 1},
		{"remote host rejected", "", "tcp://localhost:2375", "tcp://localhost:2375", false, 0},
		{"relative socket rejected", "", "unix://relative", "unix://relative", false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			configurationPath := fakeCLI(t, fakeCLIConfig{Host: test.reported, DataRoot: t.TempDir()})
			t.Setenv("DOCKER_CONTEXT", test.context)
			t.Setenv("DOCKER_HOST", test.host)
			t.Setenv("JEV_API_KEY", "fixture-secret")
			t.Setenv("SSHHOST", "fixture-private")
			t.Setenv("DOCKER_CUSTOM_HEADERS", "secret=value")
			t.Setenv("DOCKER_TLS_VERIFY", "1")
			_, err := List("")
			if (err == nil) != test.daemon {
				t.Fatalf("expected daemon=%v err=%v", test.daemon, err)
			}
			contextCalls := 0
			daemonCalls := 0
			for _, call := range calls(t, configurationPath) {
				if call.Arguments[0] == "context" {
					contextCalls++
				} else {
					daemonCalls++
					if len(call.Arguments) < 2 || call.Arguments[0] != "--host" || call.Arguments[1] != test.reported {
						t.Fatalf("endpoint not pinned: %+v", call.Arguments)
					}
				}
				for _, value := range call.Environment {
					for _, forbidden := range []string{"JEV_API_KEY=", "SSHHOST=", "SSHUSER=", "SSHKEY=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "DOCKER_CUSTOM_HEADERS=", "DOCKER_TLS_VERIFY="} {
						if strings.HasPrefix(value, forbidden) {
							t.Fatalf("inherited private/selection environment: %s", forbidden)
						}
					}
				}
			}
			if contextCalls != test.contextCalls || (!test.daemon && daemonCalls != 0) {
				t.Fatalf("context calls %d daemon calls %d", contextCalls, daemonCalls)
			}
		})
	}
}

func TestCLIIncompleteAndInvalidInventory(t *testing.T) {
	for _, name := range []string{"unicode name", "unicode project", "invalid project", "driver control", "duplicate name", "invalid JSON", "trailing JSON", "partial inventory after success", "invalid name prefix"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			container := detectedContainer(t, directory, "web")
			configuration := fakeCLIConfig{Host: "unix:///run/test.sock", DataRoot: directory, Containers: []inspectedContainer{container}}
			switch name {
			case "partial inventory after success":
				second := container
				second.ID = strings.Repeat("b", 64)
				second.Name = "/other"
				configuration.Containers = append(configuration.Containers, second)
				configuration.FailureID = second.ID
			case "invalid name prefix":
				configuration.Containers[0].Name = "/-web"
			case "unicode name":
				configuration.Containers[0].Name = "/web\u0085"
			case "unicode project":
				configuration.Containers[0].Project = "shop\u0085"
			case "invalid project":
				configuration.Containers[0].Project = "shop bad"
			case "driver control":
				configuration.Containers[0].Driver = "json-file\u0085"
			case "duplicate name":
				second := container
				second.ID = strings.Repeat("b", 64)
				configuration.Containers = append(configuration.Containers, second)
			case "invalid JSON":
				configuration.InspectOutput = "{"
			case "trailing JSON":
				raw, _ := json.Marshal(container)
				configuration.InspectOutput = string(raw) + "\n{}"
			}
			fakeCLI(t, configuration)
			inventory, err := List("")
			if err == nil || inventory != nil {
				t.Fatalf("partial or invalid inventory accepted: %+v %v", inventory, err)
			}
		})
	}
	t.Run("no CLI never guesses data-root", func(t *testing.T) {
		previous := Paths
		Paths = nil
		t.Cleanup(func() { Paths = previous })
		if _, err := List(""); !errors.Is(err, ErrNoCLI) {
			t.Fatalf("missing CLI: %v", err)
		}
	})
	t.Run("untrusted CLI executable", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "docker")
		os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0777)
		if err := validateCLIExecutable(path); err == nil {
			t.Fatal("writable CLI accepted")
		}
		link := filepath.Join(directory, "linked-docker")
		os.Symlink("/usr/bin/true", link)
		if err := validateCLIExecutable(link); err == nil {
			t.Fatal("CLI link in writable directory accepted")
		}
	})
	for _, test := range []struct {
		name, driver, pathKind, IDs, failure string
		inventoryError                       bool
	}{
		{name: "journald", driver: "journald"}, {name: "local", driver: "local"}, {name: "empty path", driver: "json-file", pathKind: "empty"}, {name: "outside data-root", driver: "json-file", pathKind: "outside"}, {name: "missing file", driver: "json-file", pathKind: "missing"}, {name: "relative path", driver: "json-file", pathKind: "relative"},
		{name: "invalid ID", IDs: "--evil", inventoryError: true}, {name: "duplicate ID", IDs: strings.Repeat("a", 64) + "\n" + strings.Repeat("a", 64), inventoryError: true}, {name: "partial inspect", failure: "inspect", inventoryError: true}, {name: "daemon error", failure: "info", inventoryError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			container := detectedContainer(t, directory, "web")
			if test.driver != "" {
				container.Driver = test.driver
			}
			switch test.pathKind {
			case "empty":
				container.LogPath = ""
			case "outside":
				container.LogPath = filepath.Join(t.TempDir(), "other.log")
				os.WriteFile(container.LogPath, nil, 0600)
			case "missing":
				os.Remove(container.LogPath)
			case "relative":
				container.LogPath = "relative.log"
			}
			fakeCLI(t, fakeCLIConfig{Host: "unix:///run/test.sock", DataRoot: directory, Containers: []inspectedContainer{container}, IDs: test.IDs, Failure: test.failure})
			inventory, err := List("")
			if test.inventoryError {
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("expected sanitized failure: %v", err)
				}
				return
			}
			if err != nil || len(inventory) != 1 || inventory[0].LogPath != "" || inventory[0].LogError == nil {
				t.Fatalf("invalid log accepted: %+v err=%v", inventory, err)
			}
		})
	}
}

func TestValidateLogPath(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "empty.log")
	os.WriteFile(regular, nil, 0600)
	link := filepath.Join(directory, "link.log")
	os.Symlink(regular, link)
	fifo := filepath.Join(directory, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path string
		valid      bool
	}{{"empty regular", regular, true}, {"missing", filepath.Join(directory, "missing"), false}, {"relative", "relative.log", false}, {"directory", directory, false}, {"FIFO", fifo, false}, {"symlink", link, false}, {"parent traversal", directory + "/../other", false}, {"newline", regular + "\n", false}} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateLogPath("", test.path); (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses file read permission; this case requires a non-root process")
		}
		unreadable := filepath.Join(directory, "unreadable.log")
		if err := os.WriteFile(unreadable, nil, 0000); err != nil {
			t.Fatal(err)
		}
		if err := ValidateLogPath("", unreadable); err == nil {
			t.Fatal("unreadable log accepted")
		}
	})
	t.Run("unsafe parent link", func(t *testing.T) {
		parent := filepath.Join(directory, "linked-parent")
		if err := os.Symlink(directory, parent); err != nil {
			t.Fatal(err)
		}
		if err := ValidateLogPath("", filepath.Join(parent, "empty.log")); err == nil {
			t.Fatal("unsafe parent link accepted")
		}
	})
	for _, name := range []string{"glob[1].log", "unicode\u0085.log"} {
		path := filepath.Join(directory, name)
		os.WriteFile(path, nil, 0600)
		if err := ValidateLogPath("", path); err == nil {
			t.Fatalf("config-incompatible filename accepted: %q", name)
		}
	}
	root := t.TempDir()
	hostPath := "/logs/app.log"
	actual := filepath.Join(root, hostPath)
	os.MkdirAll(filepath.Dir(actual), 0700)
	os.WriteFile(actual, nil, 0600)
	for _, path := range []string{hostPath, actual} {
		if err := ValidateLogPath(root, path); err != nil {
			t.Fatalf("fixture prefix failed: %v", err)
		}
	}
}

func TestCLILimitsAndChildren(t *testing.T) {
	t.Run("exact output boundary", func(t *testing.T) {
		fakeCLI(t, fakeCLIConfig{Host: "unix:///run/test.sock", OutputCommand: "context-show", OutputBytes: maxCLIOutput})
		executable, err := findCLI()
		if err != nil {
			t.Fatal(err)
		}
		inventory := cliInventory{executable: executable, environment: discoveryEnvironment(), remaining: maxCLIOutput}
		output, err := inventory.run(context.Background(), "context", "show")
		if err != nil || len(output) != maxCLIOutput || inventory.remaining != 0 {
			t.Fatalf("boundary bytes=%d remaining=%d err=%v", len(output), inventory.remaining, err)
		}
		if _, err := inventory.run(context.Background(), "info"); err == nil {
			t.Fatal("inventory continued after consuming total limit")
		}
	})
	t.Run("cumulative output cap", func(t *testing.T) {
		fakeCLI(t, fakeCLIConfig{Host: "unix:///run/test.sock", DataRoot: "/arbitrary/data-root", OutputCommand: "context-show", OutputBytes: maxCLIOutput - 10})
		executable, err := findCLI()
		if err != nil {
			t.Fatal(err)
		}
		inventory := cliInventory{executable: executable, environment: discoveryEnvironment(), remaining: maxCLIOutput}
		if _, err := inventory.run(context.Background(), "context", "show"); err != nil {
			t.Fatal(err)
		}
		if _, err := inventory.run(context.Background(), "info"); err == nil || !strings.Contains(err.Error(), "8 MiB") {
			t.Fatalf("cumulative cap: %v", err)
		}
	})
	t.Run("output overflow", func(t *testing.T) {
		fakeCLI(t, fakeCLIConfig{Host: "unix:///run/test.sock", OutputCommand: "context-show", OutputBytes: maxCLIOutput + 1})
		_, err := List("")
		if err == nil || !strings.Contains(err.Error(), "8 MiB") {
			t.Fatalf("output limit: %v", err)
		}
	})
	t.Run("deadline kills descendants", func(t *testing.T) {
		// Race-instrumented helpers restart for each command; block the first one
		// so earlier helper startup cannot consume the descendant's deadline.
		path := fakeCLI(t, fakeCLIConfig{Host: "unix:///run/test.sock", DataRoot: t.TempDir(), DelayCommand: "context-show", DelayMilliseconds: 10000, SpawnChild: true})
		previous := Timeout
		Timeout = 3 * time.Second
		t.Cleanup(func() { Timeout = previous })
		started := time.Now()
		_, err := List("")
		if err == nil || !strings.Contains(err.Error(), "did not finish") || time.Since(started) > 7*time.Second {
			t.Fatalf("deadline not enforced: %v duration=%v", err, time.Since(started))
		}
		raw, err := os.ReadFile(path + ".child")
		if err != nil {
			t.Fatalf("descendant was not started: %v", err)
		}
		pid, err := strconv.Atoi(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		stat, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		stopped, err := descendantStopped(stat, readErr)
		if err != nil {
			t.Fatal(err)
		}
		if !stopped {
			t.Fatalf("descendant remains active: %s", stat)
		}
	})
}

func descendantStopped(stat []byte, readErr error) (bool, error) {
	if readErr != nil {
		// An opened proc file can return ESRCH if the process exits before read.
		if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, syscall.ESRCH) {
			return true, nil
		}
		return false, readErr
	}
	closing := strings.LastIndex(string(stat), ")")
	if closing < 0 {
		return false, errors.New("invalid descendant process stat")
	}
	state := strings.Fields(string(stat)[closing+1:])
	if len(state) == 0 {
		return false, errors.New("missing descendant process state")
	}
	return state[0] == "Z", nil
}

func TestDescendantStopped(t *testing.T) {
	tests := []struct {
		name    string
		stat    string
		readErr error
		stopped bool
		wantErr bool
	}{
		{name: "removed before open", readErr: &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.ENOENT}, stopped: true},
		{name: "exited before read", readErr: &os.PathError{Op: "read", Path: "/proc/1/stat", Err: syscall.ESRCH}, stopped: true},
		{name: "zombie", stat: "1 (child (worker)) Z 0", stopped: true},
		{name: "running", stat: "1 (child) R 0"},
		{name: "sleeping", stat: "1 (child) S 0"},
		{name: "permission denied", readErr: syscall.EACCES, wantErr: true},
		{name: "io error", readErr: syscall.EIO, wantErr: true},
		{name: "missing name", stat: "1 Z 0", wantErr: true},
		{name: "missing state", stat: "1 (child)", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stopped, err := descendantStopped([]byte(test.stat), test.readErr)
			if stopped != test.stopped || (err != nil) != test.wantErr {
				t.Fatalf("stopped=%v err=%v; want stopped=%v error=%v", stopped, err, test.stopped, test.wantErr)
			}
		})
	}
}
