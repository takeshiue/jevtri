package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// container writes Docker's metadata for a container under root.
func container(t *testing.T, root, id, name, driver string) {
	t.Helper()
	composeContainer(t, root, id, name, driver, "")
}

// composeContainer is container with a Docker Compose project label.
func composeContainer(t *testing.T, root, id, name, driver, project string) {
	t.Helper()
	dir := filepath.Join(root, "/var/lib/docker/containers", id)
	os.MkdirAll(dir, 0o755)
	logPath := ""
	if driver == "json-file" {
		logPath = filepath.Join("/var/lib/docker/containers", id, id+"-json.log")
		os.WriteFile(filepath.Join(root, logPath), []byte(`{"log":"ok\n","stream":"stdout","time":"2026-10-01T10:00:05.1Z"}`+"\n"), 0o640)
	}
	labels := `{}`
	if project != "" {
		labels = `{"com.docker.compose.project":"` + project + `"}`
	}
	os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(`{"ID":"`+id+`","Name":"/`+name+`","LogPath":"`+logPath+`","State":{"Running":true},"Config":{"Image":"example/`+name+`:1","Labels":`+labels+`}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "hostconfig.json"), []byte(`{"LogConfig":{"Type":"`+driver+`"}}`), 0o600)
}

func journalctl(t *testing.T, root string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "usr", "bin", "journalctl"), []byte("#!/bin/sh\n"), 0o755)
}

func labels(found []Candidate) []string {
	var out []string
	for _, c := range found {
		out = append(out, c.Name+"="+c.Label())
	}
	return out
}

// IN-06: init offers Docker containers with a json-file log, and the whole
// journal only when there is no syslog or messages file.
func TestFindDockerAndJournal(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00.000000+09:00 host x: y\n"})
	container(t, root, "a1", "web", "json-file")
	container(t, root, "b2", "db", "journald")
	journalctl(t, root)
	got := strings.Join(labels(Find(FamilyDebian, root)), " ")
	if !strings.Contains(got, "docker-web=docker:web") || strings.Contains(got, "docker:db") || strings.Contains(got, "journal") {
		t.Errorf("with syslog: %s", got)
	}
	os.Remove(filepath.Join(root, "/var/log/syslog"))
	got = strings.Join(labels(Find(FamilyDebian, root)), " ")
	if !strings.Contains(got, "journal=journal:*") {
		t.Errorf("journald only: %s", got)
	}
	rendered := Render(Find(FamilyDebian, root), time.Now())
	if !strings.Contains(rendered, "[log docker-web]\npath = /var/lib/docker/containers/a1/a1-json.log\ndocker_container = web\n") || !strings.Contains(rendered, "[log journal]\njournal_unit = *\n") {
		t.Errorf("rendered:\n%s", rendered)
	}
}

// IN-07: --config-update asks about each new log (y, Enter, all) and about
// each configured log that is gone; only those sections change.
func TestUpdateAddsAndRemoves(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00.000000+09:00 host x: y\n"})
	container(t, root, "a1", "web", "json-file")
	container(t, root, "b2", "api", "json-file")
	container(t, root, "c3", "worker", "json-file")
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	original := "# my comment\n[general]\nminutes = 10\n\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n\n[log old]\n# about old\npath = /var/log/old.log\ntime_format = syslog\n\n[log gone]\ndocker_container = gone\n"
	os.WriteFile(conf, []byte(original), 0o640)

	// New: docker-api, docker-web, docker-worker (sorted by name). Answers: y,
	// Enter, n, then for the two gone logs (old, gone): y, Enter.
	var out bytes.Buffer
	err := Update(strings.NewReader("\ny\n\nn\ny\n\n"), &out, Options{Root: root, ConfigPath: conf, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(conf)
	text := string(data)
	if !strings.HasPrefix(text, configurationSample+"# my comment\n[general]\nminutes = 10\n\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n\n") {
		t.Errorf("kept lines changed:\n%s", text)
	}
	if strings.Contains(text, "[log old]") || strings.Contains(text, "about old") || !strings.Contains(text, "[log gone]") {
		t.Errorf("removal wrong:\n%s", text)
	}
	if !strings.Contains(text, "[log docker-api]\npath = /var/lib/docker/containers/b2/b2-json.log\ndocker_container = api\n") || strings.Contains(text, "docker_container = web") {
		t.Errorf("addition wrong:\n%s", text)
	}
	if info, _ := os.Stat(conf); info.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "Added 1 log") || !strings.Contains(out.String(), "Removed 1 log") {
		t.Errorf("output:\n%s", out.String())
	}

	// "all" adds the asked one and every one after it; already configured
	// containers are not offered again.
	out.Reset()
	if err := Update(strings.NewReader("\nall\n\n"), &out, Options{Root: root, ConfigPath: conf, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(conf)
	if !strings.Contains(string(data), "docker_container = web") || !strings.Contains(string(data), "docker_container = worker") || strings.Count(string(data), "docker_container = api") != 1 {
		t.Errorf("all:\n%s\n%s", data, out.String())
	}

	// Nothing new and nothing to remove except the declined one: Enter changes nothing.
	before, _ := os.ReadFile(conf)
	out.Reset()
	Update(strings.NewReader("\n"), &out, Options{Root: root, ConfigPath: conf, Now: time.Now()})
	after, _ := os.ReadFile(conf)
	if !bytes.Equal(before, after) {
		t.Errorf("declined update changed the file")
	}
}

func TestUpdateWithoutConfiguration(t *testing.T) {
	err := Update(strings.NewReader(""), &bytes.Buffer{}, Options{Root: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "none.conf")})
	if err == nil || !strings.Contains(err.Error(), "run 'jevtri init' first") {
		t.Errorf("%v", err)
	}
}

// IN-08: default groups: host logs are in system, a Compose project is
// registered as a whole in its own group, and a container without Compose is
// in a group of its own name.
func TestDefaultGroups(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{
		"/var/log/syslog":          "=2026-10-01T10:00:00.000000+09:00 host x: y\n",
		"/var/log/nginx/error.log": "=2026/10/01 10:00:00 [error] 1#1: x\n",
	})
	composeContainer(t, root, "a1", "onyx-api-1", "json-file", "onyx")
	composeContainer(t, root, "b2", "onyx-db-1", "json-file", "onyx")
	container(t, root, "c3", "ollama", "json-file")
	text := Render(Find(FamilyDebian, root), time.Now())
	for _, want := range []string{
		"[log system]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n",
		"[log nginx-error]\npath = /var/log/nginx/error.log\ntime_format = slash-ymd\ngroup = system\n",
		"[log onyx-onyx-api-1]\npath = /var/lib/docker/containers/a1/a1-json.log\ndocker_container = onyx-api-1\ndocker_project = onyx\ntime_format = docker-json\ngroup = onyx\n",
		"[log docker-ollama]\npath = /var/lib/docker/containers/c3/c3-json.log\ndocker_container = ollama\ntime_format = docker-json\ngroup = ollama\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// IN-09: --config-update offers a new Compose project once; adding it removes
// its containers registered one by one, and logs without a group get their
// default group.
func TestUpdateProjects(t *testing.T) {
	root := fakeRoot(t, ubuntu, map[string]string{"/var/log/syslog": "=2026-10-01T10:00:00.000000+09:00 host x: y\n"})
	composeContainer(t, root, "a1", "shop-api-1", "json-file", "shop")
	composeContainer(t, root, "b2", "shop-db-1", "json-file", "shop")
	container(t, root, "c3", "ollama", "json-file")
	conf := filepath.Join(t.TempDir(), "jevtri.conf")
	os.WriteFile(conf, []byte("[general]\n\n[log system]\npath = /var/log/syslog\ntime_format = rfc3339\n\n[log docker-shop-api-1]\ndocker_container = shop-api-1\n\n[log docker-ollama]\ndocker_container = ollama\n# keep this comment\n"), 0o600)
	var out bytes.Buffer
	// New: compose:shop (1 question). Groupless: system, docker-ollama (2 questions).
	if err := Update(strings.NewReader("n\ny\ny\nall\n"), &out, Options{Root: root, ConfigPath: conf, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(conf)
	text := string(data)
	for _, want := range []string{
		"[log system]\npath = /var/log/syslog\ntime_format = rfc3339\ngroup = system\n",
		"[log docker-ollama]\npath = /var/lib/docker/containers/c3/c3-json.log\ndocker_container = ollama\n# keep this comment\ntime_format = docker-json\ngroup = ollama\n",
		"[log shop-shop-api-1]\npath = /var/lib/docker/containers/a1/a1-json.log\ndocker_container = shop-api-1\ndocker_project = shop\ntime_format = docker-json\ngroup = shop\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s\n%s", want, text, out.String())
		}
	}
	if strings.Contains(text, "[log docker-shop-api-1]") {
		t.Errorf("container registered one by one was not replaced:\n%s", text)
	}
	if !strings.Contains(out.String(), "2 containers: shop-api-1, shop-db-1") {
		t.Errorf("project not shown with its containers:\n%s", out.String())
	}
}
