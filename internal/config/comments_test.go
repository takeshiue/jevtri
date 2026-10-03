package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSplitCommentPreservesRawLine(t *testing.T) {
	tests := []struct {
		line, content, comment string
	}{
		{"[log app] # note\n", "[log app]", " # note\n"},
		{"  # \"not closed\n", "", "  # \"not closed\n"},
		{"\t; 'not closed\r\n", "", "\t; 'not closed\r\n"},
		{"path = /logs/foo#bar.log", "path = /logs/foo#bar.log", ""},
		{"mask = foo ; literal", "mask = foo ; literal", ""},
		{"path = \"/logs/a #b\" \t # \"not closed\r\n", "path = \"/logs/a #b\"", " \t # \"not closed\r\n"},
		{`mask = "say\" # literal" # note`, `mask = "say\" # literal"`, " # note"},
		{`mask = "ends\\" # note`, `mask = "ends\\"`, " # note"},
		{`mask = bare"quote#literal`, `mask = bare"quote#literal`, ""},
		{"mask =\t# no value", "mask =", "\t# no value"},
	}
	for _, test := range tests {
		content, comment, err := SplitComment(test.line)
		if err != nil || content != test.content || comment != test.comment || content+comment != test.line {
			t.Errorf("%q: content=%q comment=%q err=%v", test.line, content, comment, err)
		}
		stripped, err := StripComment(test.line)
		if err != nil || stripped != content {
			t.Errorf("StripComment %q: %q %v", test.line, stripped, err)
		}
	}
}

func TestInlineCommentsParseQuotedValuesAndRegex(t *testing.T) {
	text := `[general] # general
minutes = 10 # minutes
max_bytes = 30000	# budget
sent_log = '/logs/sent # record.log' # record
[log app] # label
path = "/logs/a #b.log" # actual path
time_format = "%Y-%m-%d %H:%M:%S" # custom format
timezone = 'UTC' # zone
group = billing # first
group = "incident" # second
mask = "order-\d+ # keep" # regex
mask = 'can\'t # token' # escaped delimiter
mask = ^say"hi#there$ # bare literal quote
mask = foo ; literal # semicolon is literal
`
	text = strings.ReplaceAll(text, `\t#`, "\t#")
	for _, ending := range []string{"LF", "CRLF", "EOF"} {
		t.Run(ending, func(t *testing.T) {
			input := text
			if ending == "CRLF" {
				input = strings.ReplaceAll(input, "\n", "\r\n")
			} else if ending == "EOF" {
				input = strings.TrimSuffix(input, "\n")
			}
			cfg, err := Parse(strings.NewReader(input), "comments.conf")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Minutes != 10 || cfg.MaxBytes != 30000 || cfg.SentLog != "/logs/sent # record.log" || len(cfg.Logs) != 1 {
				t.Fatalf("unexpected general values: %+v", cfg)
			}
			log := cfg.Logs[0]
			if log.Name != "app" || log.Path != "/logs/a #b.log" || log.TimeFormat != "%Y-%m-%d %H:%M:%S" || log.Location.String() != "UTC" || strings.Join(log.Groups, ",") != "billing,incident" {
				t.Fatalf("unexpected log: %+v", log)
			}
			wantMasks := []string{`order-\d+ # keep`, `can\'t # token`, `^say"hi#there$`, "foo ; literal"}
			if !reflect.DeepEqual(log.Masks, wantMasks) {
				t.Fatalf("regex bytes changed: %q", log.Masks)
			}
			for index, target := range []string{"order-123 # keep", "can't # token", `say"hi#there`, "foo ; literal"} {
				pattern, err := regexp.Compile(log.Masks[index])
				if err != nil || !pattern.MatchString(target) {
					t.Errorf("regex %q target %q: %v", log.Masks[index], target, err)
				}
			}
		})
	}
}

func TestInlineCommentsLiteralHashAndOldConfiguration(t *testing.T) {
	cfg, err := Parse(strings.NewReader("[log hash]\npath = /logs/foo#bar.log\nmask = foo#bar\nmask = bare\"quote\nmask = name=\"\nmask = [\"']\n"), "hash.conf")
	if err != nil || cfg.Logs[0].Path != "/logs/foo#bar.log" || strings.Join(cfg.Logs[0].Masks, ",") != "foo#bar,bare\"quote,name=\",[\"']" {
		t.Fatalf("bare values changed: %+v %v", cfg, err)
	}
	before, err := Parse(strings.NewReader(sample), "old.conf")
	if err != nil {
		t.Fatal(err)
	}
	withComments := strings.ReplaceAll(sample, "path = /opt/app/app.log", "path = /opt/app/app.log # extra")
	after, err := Parse(strings.NewReader(withComments), "old.conf")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("old configuration changed: %v", err)
	}
}

func TestInlineCommentsInvalidQuoteHasLine(t *testing.T) {
	for _, assignment := range []string{`mask = "open`, `mask = 'open`, `mask = "closed"junk`, `mask = "closed"#not-separated`, `mask = "escaped\"`, "mask = \"embedded\rnewline\"", "mask = \u00a0\"open"} {
		_, err := Parse(strings.NewReader("[log app]\npath = /logs/app\n"+assignment), "bad.conf")
		var configError *Error
		if !errors.As(err, &configError) || configError.File != "bad.conf" || configError.Line != 3 || !strings.Contains(configError.Msg, "quoted") {
			t.Errorf("%q: %v", assignment, err)
		}
	}
	if _, _, err := SplitComment("mask = \"multi\nline\""); err == nil {
		t.Fatal("multiline quoted helper value accepted")
	}
}

func TestInlineCommentsFormatValueRoundTrip(t *testing.T) {
	values := []string{
		"", "plain#literal", "foo # literal", "foo' # literal", "foo\" # literal",
		`"leading quote`, `'leading quote`, `name="`, `["']+`, `order-\d+ # literal`,
		`trailing\`, " leading space ", "\u00a0leading unicode space\u00a0", "tab\t#literal",
		`escaped\" #literal`, `both\" and\' #literal`, `foo #bar\\`,
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			encoded, err := FormatValue(value)
			if err != nil {
				t.Fatal(err)
			}
			text := "[log app]\npath = /logs/app\nmask = " + encoded + " # annotation\n"
			cfg, err := Parse(strings.NewReader(text), "roundtrip.conf")
			if err != nil || len(cfg.Logs[0].Masks) != 1 || cfg.Logs[0].Masks[0] != value {
				t.Fatalf("value %q encoded %q: %+v %v", value, encoded, cfg, err)
			}
		})
	}
	path := "/srv/log dir #file/current.log"
	format := "%Y-%m-%d %H:%M:%S # literal"
	encodedPath, err := FormatValue(path)
	if err != nil {
		t.Fatal(err)
	}
	encodedFormat, err := FormatValue(format)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(strings.NewReader("[log app]\npath = "+encodedPath+"\ntime_format = "+encodedFormat+"\n"), "saved.conf")
	if err != nil || cfg.Logs[0].Path != path || cfg.Logs[0].TimeFormat != format {
		t.Fatalf("path/format changed: %+v %v", cfg, err)
	}
	for _, value := range []string{`foo"' #value`, `foo #value\`, `"leading\`, "bad\nvalue", "bad\rvalue", "bad\x00value", "bad\x1bvalue", "bad\u0085value"} {
		if _, err := FormatValue(value); err == nil {
			t.Errorf("unrepresentable/control value accepted: %q", value)
		}
	}
}

func TestInlineCommentsCannotBypassDockerValidation(t *testing.T) {
	for _, path := range []string{"/logs/a\x00b", "/logs/a\x1bb", "/logs/a\u0085b", "/logs/*.json"} {
		text := "[log app] # log\npath = \"" + path + "\" # path\ndocker_container = web # metadata\n"
		if _, err := Parse(strings.NewReader(text), "control.conf"); err == nil {
			t.Errorf("quoted invalid Docker path accepted: %q", path)
		}
	}
	if _, err := Parse(strings.NewReader("[log app]\npath = /logs/app\ngroup = \"bad\x1bgroup\" # group\n"), "control.conf"); err == nil {
		t.Fatal("quoted control group accepted")
	}
}

func TestInlineCommentsLoadLeavesOriginalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original.conf")
	content := []byte("; old comment\n[log app] # log\npath = '/logs/a #b.log' # path without final newline")
	if err := os.WriteFile(path, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(content) {
		t.Fatalf("original bytes changed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("original mode changed: %v", err)
	}
}
