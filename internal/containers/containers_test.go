package containers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/takeshiue/jevtri/internal/config"
	"github.com/takeshiue/jevtri/internal/docker"
)

func put(t *testing.T, root, id, name, driver, project string) {
	t.Helper()
	dir := filepath.Join(root, docker.ContainersDir, id)
	os.MkdirAll(dir, 0o755)
	logPath := ""
	if driver == "json-file" {
		logPath = filepath.Join(docker.ContainersDir, id, id+"-json.log")
		os.WriteFile(filepath.Join(root, logPath), nil, 0o640)
	}
	labels := `{}`
	if project != "" {
		labels = `{"com.docker.compose.project":"` + project + `"}`
	}
	os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(`{"ID":"`+id+`","Name":"/`+name+`","LogPath":"`+logPath+`","Config":{"Image":"x","Labels":`+labels+`}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "hostconfig.json"), []byte(`{"LogConfig":{"Type":"`+driver+`"}}`), 0o600)
}

// LR-08: a registered project becomes one log per container found now
// (new containers included, other drivers noted); what is not registered is
// reported, not read.
func TestExpandAndUncovered(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a1", "shop-api-1", "json-file", "shop")
	put(t, root, "b2", "shop-db-1", "json-file", "shop")
	put(t, root, "c3", "shop-cache-1", "journald", "shop")
	put(t, root, "d4", "blog-web-1", "json-file", "blog")
	put(t, root, "e5", "ollama", "json-file", "")
	put(t, root, "f6", "tool", "json-file", "")
	logs := []config.Log{
		{Name: "shop", DockerProject: "shop", Groups: []string{"shop"}},
		{Name: "tool", DockerContainer: "tool"},
		{Name: "gone", DockerProject: "gone"},
	}
	expanded, notes := Expand(logs, root)
	var names []string
	for _, l := range expanded {
		names = append(names, l.Name+"="+l.Label()+"@"+strings.Join(l.Groups, ","))
	}
	got := strings.Join(names, " ")
	want := "shop/shop-api-1=docker:shop-api-1@shop shop/shop-db-1=docker:shop-db-1@shop tool=docker:tool@ gone=compose:gone@"
	if got != want {
		t.Errorf("expanded:\n got %s\nwant %s", got, want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "shop-cache-1") {
		t.Errorf("notes: %v", notes)
	}
	projects, single := Uncovered(expanded, root)
	if strings.Join(projects, ",") != "blog" || strings.Join(single, ",") != "ollama" {
		t.Errorf("uncovered: %v %v", projects, single)
	}
	// Before expansion, a registered project still covers its containers.
	if projects, single := Uncovered(logs, root); strings.Join(projects, ",") != "blog" || strings.Join(single, ",") != "ollama" {
		t.Errorf("uncovered before expansion: %v %v", projects, single)
	}
	if err := ProjectError(root, "gone"); err == nil {
		t.Error("missing project has no error")
	}
	if g := DefaultGroup(root, "shop-db-1"); g != "shop" {
		t.Errorf("default group %q", g)
	}
	if g := DefaultGroup(root, "ollama"); g != "ollama" {
		t.Errorf("default group %q", g)
	}

	offers := Offers(root)
	var labels []string
	for _, o := range offers {
		labels = append(labels, o.Label()+":"+strings.Join(o.Members, "+"))
	}
	if strings.Join(labels, " ") != "compose:blog:blog-web-1 docker:ollama: compose:shop:shop-api-1+shop-db-1 docker:tool:" {
		t.Errorf("offers: %v", labels)
	}
}

func TestExpandedNamesMustRemainUnique(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a1", "api", "json-file", "shop")
	logs := []config.Log{{Name: "shop", DockerProject: "shop"}, {Name: "shop/api", Path: "/var/log/host"}}
	expanded, _ := Expand(logs, root)
	if err := ValidateNames(expanded); err == nil || !strings.Contains(err.Error(), "shop/api") {
		t.Fatalf("expected expanded collision rejection, got %v", err)
	}
	logs[1].Name = "host"
	expanded, _ = Expand(logs, root)
	if err := ValidateNames(expanded); err != nil {
		t.Fatal(err)
	}
}
