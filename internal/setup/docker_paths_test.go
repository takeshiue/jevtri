package setup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/containers"
	"github.com/takeshiue/jevtri/internal/docker"
)

func TestDockerPathProjectRegistrationAndGroups(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	composeContainer(t, root, "a1", "api", "json-file", "shop")
	composeContainer(t, root, "b2", "db", "json-file", "shop")
	opts := jevOptions(t, &fakeDetector{}, nil)
	opts.Root = root
	var output bytes.Buffer
	if err := Run(strings.NewReader("\n\nbilling\npayments\n\n"), &output, opts); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logs) != 2 {
		t.Fatalf("logs=%d", len(cfg.Logs))
	}
	for _, entry := range cfg.Logs {
		if entry.DockerProject != "shop" || entry.DockerContainer == "" || entry.Path == "" || strings.HasPrefix(entry.Path, root) {
			t.Errorf("invalid registration: %+v", entry)
		}
		if strings.Join(entry.Groups, ",") != "billing,payments" || entry.TimeFormat != "docker-json" {
			t.Errorf("lost groups/format: %+v", entry)
		}
	}
	if strings.Count(output.String(), "Groups for compose:shop:") != 1 {
		t.Fatalf("project group UI not shown once: %s", output.String())
	}
}

func TestDockerPathBulkDefaultGroups(t *testing.T) {
	for _, answer := range []string{"y\n", "n\ncustom\n\n\n", ""} {
		candidates := []Candidate{{Path: "/a", Group: "a"}, {Path: "/b", Group: "b"}}
		var output bytes.Buffer
		if err := chooseCandidateGroups(bufio.NewReader(strings.NewReader(answer)), &output, candidates); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "/a: a") || !strings.Contains(output.String(), "/b: b") {
			t.Fatal(output.String())
		}
		if answer == "y\n" && strings.Contains(output.String(), "Group> ") {
			t.Fatal("bulk yes prompted individually")
		}
		if answer == "" && !strings.Contains(output.String(), "Group> ") {
			t.Fatal("EOF approved bulk")
		}
		if strings.HasPrefix(answer, "n") && strings.Join(candidates[0].Groups, ",") != "custom" {
			t.Fatal(candidates)
		}
	}
}

func TestDockerPathAllUpdatesKeepManualExplicit(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/manual.log": "="})
	composeContainer(t, root, "a1", "api", "json-file", "")
	composeContainer(t, root, "b2", "db", "json-file", "")
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[log api]\ndocker_container = api\ngroup = app\n[log db]\ndocker_container = db\ngroup = app\n[log lost]\ndocker_container = lost\ngroup = app\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := docker.List(root)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	changes, err := chooseDockerChanges(bufio.NewReader(strings.NewReader("all\n/manual.log\n")), &output, cfg, inventory, Options{Root: root, ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Paths) != 3 || changes.Paths["lost"] != "/manual.log" || strings.Count(output.String(), "[y/N/all]") != 1 || !strings.Contains(output.String(), "Path> ") {
		t.Fatalf("%+v %s", changes, output.String())
	}
}

func TestDockerPathExplicitFormatsWithoutPathChanges(t *testing.T) {
	for _, item := range []struct {
		name, format, answer string
		changed              bool
	}{{"default", "", "y\n", true}, {"blank", "time_format = \n", "y\n", true}, {"decline", "", "n\n", false}, {"custom", "time_format = rfc3339\n", "", false}} {
		t.Run(item.name, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, nil)
			composeContainer(t, root, "a1", "api", "json-file", "")
			path := filepath.Join(t.TempDir(), "jevtri.conf")
			before := "[log api]\npath = /var/lib/docker/containers/a1/a1-json.log\ndocker_container = api\n" + item.format + "group = app\n"
			if err := os.WriteFile(path, []byte(before), 0640); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Update(strings.NewReader(item.answer), &output, Options{Root: root, ConfigPath: path}); err != nil {
				t.Fatalf("%v %s", err, output.String())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if item.changed && !strings.Contains(string(data), "time_format = docker-json\n") {
				t.Fatal(string(data))
			}
			if !item.changed && string(data) != configurationSample+before {
				t.Fatalf("declined/custom was changed: %s", data)
			}
		})
	}
}

func TestDockerPathUpdatePreservesSettingsAndDecline(t *testing.T) {
	for _, answer := range []string{"y\n", "n\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{"/custom/old.json": "="})
			composeContainer(t, root, "newid", "api", "json-file", "shop")
			opts := jevOptions(t, &fakeDetector{}, nil)
			opts.Root = root
			before := "# before\n[log app]\n# preserve comment\npath = /custom/old.json\ndocker_container = api\ndocker_project = shop\ntime_format = docker-json\ntimezone = Asia/Tokyo\nmask = CUSTOMER-[0-9]+\ngroup = billing\ngroup = payments\n"
			if err := os.WriteFile(opts.ConfigPath, []byte(before), 0640); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Update(strings.NewReader(answer), &output, opts); err != nil {
				t.Fatalf("%v\n%s", err, output.String())
			}
			data, err := os.ReadFile(opts.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			wanted := before
			if answer == "y\n" {
				wanted = strings.Replace(before, "path = /custom/old.json\n", "path = /var/lib/docker/containers/newid/newid-json.log\n", 1)
			}
			if string(data) != configurationSample+wanted {
				t.Fatalf("unexpected change:\n%s", data)
			}
			info, err := os.Stat(opts.ConfigPath)
			if err != nil || info.Mode().Perm() != 0640 {
				t.Fatalf("mode changed: %v", err)
			}
		})
	}
}

func TestDockerPathMembershipUpdateKeepsSharedGroups(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	composeContainer(t, root, "a1", "api", "json-file", "shop")
	composeContainer(t, root, "b2", "db", "json-file", "shop")
	opts := jevOptions(t, &fakeDetector{}, nil)
	opts.Root = root
	before := "[log existing]\npath = /var/lib/docker/containers/a1/a1-json.log\ndocker_container = api\ndocker_project = shop\ntime_format = docker-json\ngroup = billing\ngroup = payments\n"
	if err := os.WriteFile(opts.ConfigPath, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Update(strings.NewReader("all\n\n"), &output, opts); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logs) != 2 {
		t.Fatalf("logs=%d", len(cfg.Logs))
	}
	for _, entry := range cfg.Logs {
		if strings.Join(entry.Groups, ",") != "billing,payments" {
			t.Fatalf("groups changed: %+v", entry)
		}
	}
	data, err := os.ReadFile(opts.ConfigPath)
	if err != nil || !strings.HasPrefix(string(data), configurationSample+before) {
		t.Fatalf("existing section changed: %v", err)
	}
}

func TestDockerPathLegacyProjectPreservesSharedPolicy(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	composeContainer(t, root, "a1", "api", "json-file", "shop")
	composeContainer(t, root, "b2", "db", "json-file", "shop")
	opts := jevOptions(t, &fakeDetector{}, nil)
	opts.Root = root
	before := "[log old]\n# shared comment\ndocker_project = shop\ntime_format = docker-json\ntimezone = Asia/Tokyo\nmask = CUSTOMER-[0-9]+\ngroup = billing\ngroup = payments\n"
	if err := os.WriteFile(opts.ConfigPath, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("y\n"), &bytes.Buffer{}, opts); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logs) != 2 {
		t.Fatalf("logs=%d", len(cfg.Logs))
	}
	for _, entry := range cfg.Logs {
		if entry.Path == "" || entry.DockerContainer == "" || entry.Timezone != "Asia/Tokyo" || strings.Join(entry.Groups, ",") != "billing,payments" || strings.Join(entry.Masks, ",") != "CUSTOMER-[0-9]+" {
			t.Errorf("lost policy: %+v", entry)
		}
	}
	data, err := os.ReadFile(opts.ConfigPath)
	if err != nil || strings.Count(string(data), "# shared comment") != 2 {
		t.Fatalf("comment lost: %v", err)
	}
}

func TestDockerPathLegacyProjectCollisionStopsAll(t *testing.T) {
	root := fakeRoot(t, ubuntu, nil)
	composeContainer(t, root, "a1", "api", "json-file", "shop")
	opts := jevOptions(t, &fakeDetector{}, nil)
	opts.Root = root
	before := "[log project]\ndocker_project = shop\nmask = COMMON-[0-9]+\n\n[log individual]\ndocker_container = api\nmask = PRIVATE-[0-9]+\n"
	if err := os.WriteFile(opts.ConfigPath, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	err := Update(strings.NewReader("y\n"), &bytes.Buffer{}, opts)
	if err == nil || !strings.Contains(err.Error(), "individual registration") {
		t.Fatalf("collision not rejected: %v", err)
	}
	data, readError := os.ReadFile(opts.ConfigPath)
	if readError != nil || string(data) != before {
		t.Fatalf("configuration changed: %v", readError)
	}
	info, err := os.Stat(opts.ConfigPath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %v", err)
	}
}

func TestDockerPathManualFallbackAndInvalidInput(t *testing.T) {
	for _, path := range []string{"/custom/readable.json", "relative.json", "/custom/missing.json"} {
		t.Run(path, func(t *testing.T) {
			root := fakeRoot(t, ubuntu, map[string]string{"/custom/readable.json": "="})
			opts := jevOptions(t, &fakeDetector{}, nil)
			opts.Root = root
			before := "[log old]\ndocker_container = api\ngroup = billing\n"
			if err := os.WriteFile(opts.ConfigPath, []byte(before), 0640); err != nil {
				t.Fatal(err)
			}
			err := Update(strings.NewReader(path+"\n"), &bytes.Buffer{}, opts)
			data, readError := os.ReadFile(opts.ConfigPath)
			if readError != nil {
				t.Fatal(readError)
			}
			if path == "/custom/readable.json" {
				if err != nil || !strings.Contains(string(data), "path = "+path+"\n") {
					t.Fatalf("manual registration: %v", err)
				}
			} else {
				if err == nil || string(data) != before {
					t.Fatalf("invalid path was saved: %v", err)
				}
			}
		})
	}
}

func TestDockerPathFinalValidationAndControlCharacters(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/custom/newline\npath = evil": "=sample\n"})
	if err := validateCandidates([]Candidate{{Name: "bad", Path: "/custom/newline\npath = evil"}}, root); err == nil {
		t.Fatal("control-character path accepted")
	}
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[log keep]\npath = /custom/old\n"
	if err := os.WriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	changes := dockerChanges{Paths: map[string]string{}, Projects: map[string][]Candidate{}}
	candidates := []Candidate{{Name: "new", Path: "/custom/missing", DockerContainer: "api", TimeFormat: "docker-json"}}
	if err := saveRegisteredUpdate(path, nil, nil, candidates, changes, root); err == nil {
		t.Fatal("final missing path accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != before {
		t.Fatalf("failed validation changed config: %v", err)
	}
}

func TestDockerPathExistingGrouplessAcceptsMultipleGroups(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00Z host ok\n"})
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(strings.NewReader("y\nops\nincident\n\n"), &bytes.Buffer{}, Options{Root: root, ConfigPath: path}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Logs[0].Groups, ",") != "ops,incident" {
		t.Fatalf("groups=%v", cfg.Logs[0].Groups)
	}
}

func TestDockerPathFlattenNamesCannotCollide(t *testing.T) {
	candidates := []Candidate{{Name: "shop", DockerProject: "shop", MemberLogs: []containers.Member{{Name: "api", Path: "/logs/api"}}, Group: "shop"}}
	flat, err := flattenCandidates(candidates, []config.Log{{Name: "shop-api", Path: "/logs/host"}})
	if err != nil || len(flat) != 1 || flat[0].Name != "shop-api-2" {
		t.Fatalf("collision: %v, %v", flat, err)
	}
}

func TestDockerPathAtomicInitNeverOverwritesOrLeavesTemps(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "jevtri.conf")
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, content := range []string{"first complete configuration\n", "second complete configuration\n"} {
		workers.Add(1)
		go func(content string) { defer workers.Done(); <-start; results <- write(path, content) }(content)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, refusals := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrExists) {
			refusals++
		} else {
			t.Fatalf("unexpected write failure: %v", err)
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("successes=%d refusals=%d", successes, refusals)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first complete configuration\n" && string(data) != "second complete configuration\n" {
		t.Fatalf("partial data: %q", data)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("mode changed: %v", err)
	}
	before := append([]byte(nil), data...)
	if err := write(path, "replacement\n"); !errors.Is(err, ErrExists) {
		t.Fatalf("existing config overwritten: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(data, before) {
		t.Fatalf("existing contents changed: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remained: %v, %v", entries, err)
	}
}

func TestDockerPathGrouplessUsesCapturedInventory(t *testing.T) {
	cfg := &config.Config{}
	var inventory []docker.Container
	for index := 0; index < 44; index++ {
		name := fmt.Sprintf("container-%02d", index)
		cfg.Logs = append(cfg.Logs, config.Log{Name: name, DockerContainer: name})
		inventory = append(inventory, docker.Container{Name: name, Project: "captured-project"})
	}
	missing := groupless(cfg, nil, inventory)
	if len(missing) != 44 {
		t.Fatalf("groups=%d", len(missing))
	}
	for _, entry := range missing {
		if entry.group != "captured-project" {
			t.Fatalf("snapshot was not used: %s", entry.group)
		}
	}
}

func TestDockerPathManualNewRegistrationWithoutInventory(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/custom/api.json": "={\"log\":\"sample\\n\",\"stream\":\"stdout\",\"time\":\"2026-10-03T00:00:00Z\"}\n"})
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[general]\n# keep comment\n"
	if err := os.WriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Update(strings.NewReader("/custom/api.json\n\nmanual-service\n\n"), &output, Options{Root: root, ConfigPath: path}); err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Logs) != 1 || cfg.Logs[0].Path != "/custom/api.json" || cfg.Logs[0].TimeFormat != "docker-json" || strings.Join(cfg.Logs[0].Groups, ",") != "manual-service" {
		t.Fatalf("manual new log not registered: %+v", cfg.Logs)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), configurationSample+before) {
		t.Fatalf("existing lines changed: %v", err)
	}
}

func TestDockerPathInlineCommentsAndSampleOnce(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/old.json": "="})
	composeContainer(t, root, "a1", "api", "json-file", "")
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "# user heading\n[log api]  # header annotation\npath = /old.json  # path annotation\ndocker_container = api # metadata annotation\ntime_format =   # format annotation\ngroup = app # group annotation\nmask = CUSTOMER-[0-9]+ # mask annotation\n"
	if err := os.WriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		var output bytes.Buffer
		if err := Update(strings.NewReader("y\n"), &output, Options{Root: root, ConfigPath: path}); err != nil {
			t.Fatalf("%v %s", err, output.String())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := configurationSample + strings.Replace(strings.Replace(before, "path = /old.json", "path = /var/lib/docker/containers/a1/a1-json.log", 1), "time_format =   #", "time_format = docker-json   #", 1)
	if string(data) != want {
		t.Fatalf("comments or sample changed: %s", data)
	}
	if strings.Count(string(data), "# Configuration syntax example (inactive):") != 1 {
		t.Fatal("duplicate sample")
	}
	cfg, err := config.Load(path)
	if err != nil || len(cfg.Logs) != 1 || cfg.Logs[0].Masks[0] != "CUSTOMER-[0-9]+" {
		t.Fatalf("sample/comment became configuration: %v", err)
	}
}

func TestDockerPathQuotedSerializationKeepsEffectivePath(t *testing.T) {
	for _, logPath := range []string{"/srv/log dir #file/current.log", "/srv/log#file/current.log"} {
		root := fakeRoot(t, ubuntu, map[string]string{logPath: "="})
		candidate := Candidate{Name: "api", Path: logPath, DockerContainer: "api", TimeFormat: "docker-json", Group: "app"}
		if err := validateCandidates([]Candidate{candidate}, root); err != nil {
			t.Fatal(err)
		}
		parsed, err := config.Parse(strings.NewReader(Render([]Candidate{candidate}, time.Time{})), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSerializedCandidates(parsed, []Candidate{candidate}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "jevtri.conf")
		before := "[log api] # keep header\npath = /old.json  # keep path\ndocker_container = api\ntime_format = docker-json\ngroup = app\n"
		if err := os.WriteFile(path, []byte(before), 0600); err != nil {
			t.Fatal(err)
		}
		if err := saveRegisteredUpdate(path, nil, nil, nil, dockerChanges{Paths: map[string]string{"api": logPath}}, root); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil || cfg.Logs[0].Path != logPath {
			t.Fatalf("%v %+v", err, cfg)
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), "  # keep path") {
			t.Fatal("inline comment removed")
		}
	}
	root := fakeRoot(t, ubuntu, nil)
	path := filepath.Join(t.TempDir(), "jevtri.conf")
	before := "[log api]\npath = /old.json\ndocker_container = api\ntime_format = docker-json\ngroup = app\n"
	if err := os.WriteFile(path, []byte(before), 0640); err != nil {
		t.Fatal(err)
	}
	if err := saveRegisteredUpdate(path, nil, nil, nil, dockerChanges{Paths: map[string]string{"api": "/srv/ #both\"'quotes/"}}, root); err == nil {
		t.Fatal("unrepresentable path accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != before {
		t.Fatal("failed serialization changed configuration")
	}
}
