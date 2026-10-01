package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestHelpLanguage(t *testing.T) {
	cases := []struct {
		lang string
		env  map[string]string
		want string
	}{
		{"", nil, "en"},
		{"", map[string]string{"LANG": "C"}, "en"},
		{"", map[string]string{"LANG": "ja_JP.UTF-8"}, "ja"},
		{"", map[string]string{"LANG": "zh_CN.UTF-8"}, "zh-CN"},
		{"", map[string]string{"LANG": "zh_SG.UTF-8"}, "zh-CN"},
		{"", map[string]string{"LANG": "zh_Hans"}, "zh-CN"},
		{"", map[string]string{"LANG": "zh_TW.UTF-8"}, "en"}, // Traditional Chinese is not offered
		{"", map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "ja_JP.UTF-8"}, "ja"},
		{"", map[string]string{"LC_ALL": "C", "LC_MESSAGES": "ja_JP.UTF-8", "LANG": "ja_JP.UTF-8"}, "en"},
		{"zh-CN", map[string]string{"LANG": "ja_JP.UTF-8"}, "zh-CN"},
		{"en", map[string]string{"LANG": "ja_JP.UTF-8"}, "en"},
	}
	for _, c := range cases {
		got, err := helpLanguage(c.lang, func(name string) string { return c.env[name] })
		if err != nil || got != c.want {
			t.Errorf("helpLanguage(%q, %v) = %q, %v; want %q", c.lang, c.env, got, err, c.want)
		}
	}
	if _, err := helpLanguage("fr", func(string) string { return "" }); err == nil {
		t.Error("--lang fr was accepted")
	}
}

// LG-01: every language lists the same options, the same facts about sending,
// and the same exit codes.
func TestHelpTextsMatch(t *testing.T) {
	token := regexp.MustCompile(`--?[a-z][a-z-]*|\binit\b|\breport\b|jevtri\(1\)|api\.typesafe\.ai|/[a-z/.]*[a-z]|\b[0-3] `)
	english := token.FindAllString(helpTexts["en"], -1)
	if len(english) < 25 {
		t.Fatalf("too few tokens in the English help: %v", english)
	}
	for lang, text := range helpTexts {
		if got := token.FindAllString(text, -1); strings.Join(got, "|") != strings.Join(english, "|") {
			t.Errorf("%s help differs from English:\n got  %v\n want %v", lang, got, english)
		}
	}
}

func TestHelpByLocaleAndFlag(t *testing.T) {
	f := newFixture(t, "")
	var out bytes.Buffer
	env := environment{stdout: &out, stderr: &f.stderr, getenv: func(name string) string {
		if name == "LANG" {
			return "ja_JP.UTF-8"
		}
		return ""
	}}
	if code := run([]string{"--help"}, env); code != exitOK || !strings.Contains(out.String(), "確率では") {
		t.Errorf("LANG=ja_JP.UTF-8: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"--help", "--lang", "zh-CN"}, env); code != exitOK || !strings.Contains(out.String(), "概率") {
		t.Errorf("--lang zh-CN: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"--help", "--lang", "fr"}, env); code != exitUsage {
		t.Errorf("--lang fr: exit %d", code)
	}
}
