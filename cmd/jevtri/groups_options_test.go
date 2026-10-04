package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/takeshiue/jevtri/internal/jev"
)

func TestGroupSelectionOptions(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
		all  bool
	}{
		{"comma", []string{"--group", "shop,blog"}, []string{"shop", "blog"}, false},
		{"trim", []string{"--group", " shop , blog "}, []string{"shop", "blog"}, false},
		{"repeated and duplicate", []string{"--group", "blog,shop,blog", "--group", "shop,db-v2_1.prod"}, []string{"blog", "shop", "db-v2_1.prod"}, false},
		{"literal all remains a name", []string{"--group", "all"}, []string{"all"}, false},
		{"all flag", []string{"--all-groups"}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, rest, err := parseOptions(test.args)
			if err != nil || len(rest) != 0 || !reflect.DeepEqual(options.groups, test.want) || options.allGroups != test.all {
				t.Fatalf("options=%+v rest=%v err=%v", options, rest, err)
			}
		})
	}
	for _, value := range []string{"", " ", ",shop", "shop,", "shop,,blog", "shop, ,blog", "shop,project name", "shop,*", "shop,../blog", "shop,日本語"} {
		t.Run("reject "+value, func(t *testing.T) {
			if _, _, err := parseOptions([]string{"--group", value}); err == nil {
				t.Fatal("invalid group selection accepted")
			}
		})
	}
	for _, args := range [][]string{{"--group", "shop", "--all-groups"}, {"--all-groups", "--group", "shop,blog"}} {
		if _, _, err := parseOptions(args); err == nil || !strings.Contains(err.Error(), "cannot be used") {
			t.Fatalf("conflicting selection accepted: %v err=%v", args, err)
		}
	}
}

type groupingFixture struct {
	conf, key, dir string
	fake           *fakeGroupAsker
	stdout, stderr bytes.Buffer
	created        int
}

func newGroupingFixture(t *testing.T) *groupingFixture {
	t.Helper()
	f := &groupingFixture{dir: t.TempDir(), fake: &fakeGroupAsker{}}
	f.conf = filepath.Join(f.dir, "jevtri.conf")
	f.key = filepath.Join(f.dir, "api-key")
	if err := os.WriteFile(f.key, []byte("test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	fmt.Fprintf(&content, "[general]\nsent_log = %s\n", filepath.Join(f.dir, "sent.log"))
	for _, log := range []struct{ name, group string }{
		{"sys", "system"}, {"shop", "shop"}, {"blog", "blog"},
		{"shared", "shop,blog"}, {"ungrouped", ""}, {"quiet", "quiet"},
	} {
		line := "2026-09-28 03:02:00 ERROR example\n"
		if log.name == "quiet" {
			line = "2026-09-27 03:02:00 ERROR outside window\n"
		}
		path := filepath.Join(f.dir, log.name+".log")
		if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&content, "\n[log %s]\npath = %s\ntime_format = iso-space\n", log.name, path)
		if log.group != "" {
			for _, group := range strings.Split(log.group, ",") {
				fmt.Fprintf(&content, "group = %s\n", group)
			}
		}
	}
	if err := os.WriteFile(f.conf, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *groupingFixture) run(args ...string) int {
	zone := time.FixedZone("JST", 9*3600)
	env := environment{stdout: &f.stdout, stderr: &f.stderr,
		now:      func() time.Time { return time.Date(2026, 9, 28, 4, 0, 0, 0, zone) },
		newAsker: func(string) asker { f.created++; return f.fake },
		keyPath:  f.key, isTerminal: func() bool { return false },
		getenv: func(string) string { return "" }}
	return run(append([]string{"-c", f.conf, "-t", "03:02", "-m", "5"}, args...), env)
}

func queryLogNames(query jev.Query) []string {
	var names []string
	for _, log := range query.Logs {
		names = append(names, filepath.Base(log.Path))
	}
	sort.Strings(names)
	return names
}

func TestGroupSelectionRuns(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{"comma groups", []string{"--group", "shop,blog"}, []string{"blog.log", "shared.log", "shop.log", "sys.log", "ungrouped.log"}},
		{"mixed repeated", []string{"--group", "blog,shop", "--group", "shop"}, []string{"blog.log", "shared.log", "shop.log", "sys.log", "ungrouped.log"}},
		{"all groups", []string{"--all-groups"}, []string{"blog.log", "shared.log", "shop.log", "sys.log", "ungrouped.log"}},
		{"quiet registered group", []string{"--group", "quiet"}, []string{"sys.log", "ungrouped.log"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGroupingFixture(t)
			if code := f.run(test.args...); code != exitOK {
				t.Fatalf("exit=%d stderr=%s", code, f.stderr.String())
			}
			if len(f.fake.queries) != 1 || f.fake.queries[0].Groups {
				t.Fatalf("selection must evaluate logs directly once: %+v", f.fake.queries)
			}
			if got := queryLogNames(f.fake.queries[0]); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selected paths=%v want=%v", got, test.want)
			}
		})
	}
}

func TestGroupSelectionInvalidDoesNotSend(t *testing.T) {
	for _, args := range [][]string{
		{"--group", "shop,unknown"}, {"--group", "shop,,blog"},
		{"--group", "shop", "--all-groups"}, {"--all-groups", "--group", "blog"},
	} {
		f := newGroupingFixture(t)
		if code := f.run(args...); code != exitUsage || f.created != 0 || len(f.fake.queries) != 0 {
			t.Fatalf("args=%v code=%d created=%d queries=%d stderr=%s", args, code, f.created, len(f.fake.queries), f.stderr.String())
		}
		if _, err := os.Stat(filepath.Join(f.dir, "sent.log")); !os.IsNotExist(err) {
			t.Fatalf("invalid selection wrote a send record: %v", err)
		}
	}
}

func TestAllGroupsReportAndMissingLog(t *testing.T) {
	f := newGroupingFixture(t)
	file, err := os.OpenFile(f.conf, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(file, "\n[log removed]\npath = %s\ntime_format = iso-space\ngroup = removed\n", filepath.Join(f.dir, "removed.log"))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append: %v close: %v", err, closeErr)
	}
	if code := f.run("--all-groups", "--json"); code != exitPartial {
		t.Fatalf("exit=%d stderr=%s", code, f.stderr.String())
	}
	if len(f.fake.queries) != 1 || len(f.fake.queries[0].Logs) != 5 || !strings.Contains(f.stderr.String(), "removed.log") {
		t.Fatalf("missing log was not reported while healthy logs were ranked: queries=%+v stderr=%s", f.fake.queries, f.stderr.String())
	}
	var result struct {
		Examined []string `json:"examined_groups"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if want := []string{"blog", "quiet", "removed", "shop", "system"}; !reflect.DeepEqual(result.Examined, want) {
		t.Fatalf("examined=%v want=%v", result.Examined, want)
	}
}

func TestAllGroupsQuietDoesNotLoadKeyOrSend(t *testing.T) {
	f := newGroupingFixture(t)
	for _, name := range []string{"sys", "shop", "blog", "shared", "ungrouped"} {
		if err := os.WriteFile(filepath.Join(f.dir, name+".log"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.key = filepath.Join(f.dir, "absent-key")
	if code := f.run("--all-groups", "--json"); code != exitOK || f.created != 0 {
		t.Fatalf("quiet run: exit=%d created=%d stderr=%s", code, f.created, f.stderr.String())
	}
	var result struct {
		Examined []string `json:"examined_groups"`
	}
	if err := json.Unmarshal(f.stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Examined, []string{"blog", "quiet", "shop", "system"}) {
		t.Fatalf("quiet report lost configured groups: %+v", result)
	}
}

func TestAllGroupsStateBudget(t *testing.T) {
	f := newGroupingFixture(t)
	for _, name := range []string{"sys", "shop", "blog", "shared", "ungrouped"} {
		var lines strings.Builder
		for index := 0; index < 200; index++ {
			fmt.Fprintf(&lines, "2026-09-28 03:02:00 ERROR %s %d %s\n", name, index, strings.Repeat("\\\"payload", 30))
		}
		if err := os.WriteFile(filepath.Join(f.dir, name+".log"), []byte(lines.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := f.run("--all-groups"); code != exitOK || len(f.fake.queries) != 1 {
		t.Fatalf("exit=%d queries=%d stderr=%s", code, len(f.fake.queries), f.stderr.String())
	}
	state, err := json.Marshal(jev.BuildRequest(f.fake.queries[0]).State)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) > 40000 || len(f.fake.queries[0].Logs) != 5 {
		t.Fatalf("budget or coverage failed: state bytes=%d logs=%d stderr=%s", len(state), len(f.fake.queries[0].Logs), f.stderr.String())
	}
}
