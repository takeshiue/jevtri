package grouprank

import (
	"strings"
	"testing"
)

var members = []Member{
	{Name: "syslog", Groups: []string{"system"}},
	{Name: "old", Groups: nil},
	{Name: "shop-api", Groups: []string{"shop"}},
	{Name: "shop-db", Groups: []string{"shop"}},
	{Name: "blog-web", Groups: []string{"blog"}},
	{Name: "shared-pg", Groups: []string{"shop", "blog"}},
}

// RK-01: two or more service groups mean a first stage; the second stage is
// the system logs, logs without a group and the chosen group, each once.
func TestFirstStage(t *testing.T) {
	plan, err := Make(members, nil)
	if err != nil || !plan.FirstStage {
		t.Fatalf("%+v %v", plan, err)
	}
	if got := strings.Join(plan.CandidateNames(), ","); got != "blog,shop" {
		t.Errorf("candidates %s", got)
	}
	if got := strings.Join(Second(plan, []string{"shop"}), ","); got != "syslog,old,shop-api,shop-db,shared-pg" {
		t.Errorf("second stage %s", got)
	}
}

// RK-02: a named group replaces the first stage; an unknown one is an error;
// with at most one service group everything is ranked at once.
func TestNamedAndSkipped(t *testing.T) {
	plan, err := Make(members, []string{"blog"})
	if err != nil || plan.FirstStage || strings.Join(plan.Direct, ",") != "syslog,old,blog-web,shared-pg" {
		t.Errorf("named: %+v %v", plan, err)
	}
	if _, err := Make(members, []string{"nope"}); err == nil || !strings.Contains(err.Error(), `group "nope"`) {
		t.Errorf("unknown group: %v", err)
	}
	one := []Member{{Name: "syslog", Groups: []string{"system"}}, {Name: "a", Groups: []string{"shop"}}, {Name: "b"}}
	plan, _ = Make(one, nil)
	if plan.FirstStage || strings.Join(plan.Direct, ",") != "syslog,a,b" {
		t.Errorf("single group: %+v", plan)
	}
	plan, _ = Make([]Member{{Name: "syslog", Groups: []string{"system"}}}, nil)
	if plan.FirstStage || len(plan.Direct) != 1 {
		t.Errorf("system only: %+v", plan)
	}
}

// RK-04: the second stage takes the first group and every group within Near
// of it (EV-03), so a near tie does not drop the incident's group.
func TestChoose(t *testing.T) {
	got := Choose([]Score{{"a", 0.41}, {"b", 0.36}, {"c", 0.30}, {"d", 0.10}})
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("near: %v", got)
	}
	if got := Choose([]Score{{"a", 0.95}, {"b", 0.23}}); strings.Join(got, ",") != "a" {
		t.Errorf("clear lead: %v", got)
	}
	if Choose(nil) != nil {
		t.Error("empty")
	}
}

func TestChooseInclusiveBoundaryOnlyAllowsRounding(t *testing.T) {
	got := Choose([]Score{{"a", 1.2 / 3}, {"b", 0.9 / 3}, {"c", 0.9/3 - 1e-12}})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("boundary must include b but exclude c: %v", got)
	}
	for _, top := range []float64{0.4, 0.8, 1} {
		got := Choose([]Score{{"a", top}, {"b", top - Near}, {"c", top - Near - 1e-12}})
		if strings.Join(got, ",") != "a,b" {
			t.Fatalf("top %v: %v", top, got)
		}
	}
}
