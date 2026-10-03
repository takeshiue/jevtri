package setup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/takeshiue/jevtri/internal/jev"
)

type recordFailureDetector struct {
	requests int
	failAt   int
	sentLog  string
	apiError error
}

func (detector *recordFailureDetector) AskFormat(_ context.Context, _ jev.FormatRequest) (jev.FormatResult, error) {
	detector.requests++
	if detector.requests == detector.failAt {
		if err := os.Remove(detector.sentLog); err != nil && !os.IsNotExist(err) {
			return jev.FormatResult{}, err
		}
		if err := os.Mkdir(detector.sentLog, 0700); err != nil {
			return jev.FormatResult{}, err
		}
	}
	return jev.FormatResult{Options: []jev.Option{{Name: "iso-space", Probability: 1}}}, detector.apiError
}

func TestFormatRecordFailureStopsAndPreservesConfiguration(t *testing.T) {
	for _, caller := range []string{"new", "existing", "update"} {
		for _, apiFailure := range []bool{false, true} {
			for _, failAt := range []int{0, 1, 2} {
				t.Run(fmt.Sprintf("%s/apiFailure=%t/failAt=%d", caller, apiFailure, failAt), func(t *testing.T) {
					paths := []string{"/var/log/nginx/error.log", "/var/log/nginx/access.log", "/var/log/apache2/error.log"}
					files := map[string]string{}
					for _, path := range paths {
						files[path] = appLine
					}
					root := fakeRoot(t, ubuntu, files)
					opts := jevOptions(t, &fakeDetector{}, nil)
					opts.Root = root
					if failAt == 0 {
						if err := os.Mkdir(opts.SentLog, 0700); err != nil {
							t.Fatal(err)
						}
					}
					detector := &recordFailureDetector{failAt: failAt, sentLog: opts.SentLog}
					if apiFailure {
						detector.apiError = jev.ErrUnavailable
					}
					opts.NewDetector = func(string) Detector { return detector }
					original := "# preserve me\n[general]\nminutes = 10\n"
					if caller == "existing" {
						for index, path := range paths {
							original += fmt.Sprintf("\n[log app%d]\npath = %s\ngroup = system\n", index, path)
						}
					}
					if caller != "new" {
						if err := os.WriteFile(opts.ConfigPath, []byte(original), 0640); err != nil {
							t.Fatal(err)
						}
					}
					input := "\n\ny\n"
					var output bytes.Buffer
					var err error
					if caller == "update" {
						err = Update(strings.NewReader("all\n\ny\n"), &output, opts)
					} else {
						if caller == "existing" {
							input = "y\n"
						}
						err = Run(strings.NewReader(input), &output, opts)
					}
					if err == nil || !strings.Contains(err.Error(), "cannot record time format request") {
						t.Errorf("want record failure, got %v; output=%s", err, output.String())
					}
					if detector.requests != failAt {
						t.Errorf("requests=%d, want %d", detector.requests, failAt)
					}
					data, readError := os.ReadFile(opts.ConfigPath)
					if caller == "new" {
						if !os.IsNotExist(readError) {
							t.Errorf("new config was created: %v", readError)
						}
					} else {
						if readError != nil || string(data) != original {
							t.Errorf("configuration changed: %v, %s", readError, data)
						}
						info, statError := os.Stat(opts.ConfigPath)
						if statError != nil || info.Mode().Perm() != 0640 {
							t.Errorf("configuration mode changed: %v", statError)
						}
					}
				})
			}
		}
	}
}

func TestFormatRecordedAPIFailuresContinue(t *testing.T) {
	paths := []string{"/opt/app/one.log", "/opt/app/two.log"}
	root := fakeRoot(t, ubuntu, map[string]string{paths[0]: appLine, paths[1]: appLine})
	opts := jevOptions(t, &fakeDetector{}, nil)
	opts.Root = root
	detector := &recordFailureDetector{sentLog: opts.SentLog, apiError: jev.ErrUnavailable}
	opts.NewDetector = func(string) Detector { return detector }
	logs := []*Candidate{{Path: paths[0]}, {Path: paths[1]}}
	var output bytes.Buffer
	if err := detectWithJev(bufio.NewReader(strings.NewReader("y\n")), &output, logs, opts); err != nil {
		t.Fatal(err)
	}
	if detector.requests != 2 {
		t.Fatalf("requests=%d", detector.requests)
	}
	data, err := os.ReadFile(opts.SentLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"error":"jev could not be reached"`) != 2 {
		t.Errorf("both errors not recorded: %s", data)
	}
}
