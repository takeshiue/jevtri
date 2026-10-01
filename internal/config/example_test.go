package config

import (
	"os"
	"testing"
)

// The packaged example must stay loadable, or users who copy it get an error.
func TestPackagedExampleLoads(t *testing.T) {
	file, err := os.Open("../../packaging/jevtri.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cfg, err := Parse(file, "jevtri.conf.example")
	if err != nil {
		t.Fatalf("example does not load: %v", err)
	}
	if len(cfg.Logs) == 0 {
		t.Fatal("example has no [log] sections")
	}
	for _, log := range cfg.Logs {
		if log.Format == nil {
			t.Errorf("[log %s] has no usable time_format", log.Name)
		}
	}
}
