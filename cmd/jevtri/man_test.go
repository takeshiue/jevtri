package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var manPages = map[string]string{
	"en":    filepath.Join("..", "..", "man", "jevtri.1"),
	"ja":    filepath.Join("..", "..", "man", "ja", "jevtri.1"),
	"zh-CN": filepath.Join("..", "..", "man", "zh_CN", "jevtri.1"),
}

// The man pages carry the version in .TH, a second place besides var version.
func TestManPagesVersion(t *testing.T) {
	for lang, path := range manPages {
		page, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		header := regexp.MustCompile(`(?m)^\.TH JEVTRI 1 "[0-9-]+" "jevtri ([^"]+)"`).FindSubmatch(page)
		if header == nil || string(header[1]) != version {
			t.Errorf("%s man page: .TH does not say jevtri %s", lang, version)
		}
	}
}

// LG-01: the translations list the same options, files and exit codes, in the
// same number of sections, as the English page. Counts matter: an option
// dropped from one place in a translation must not go unnoticed.
func TestManPagesMatch(t *testing.T) {
	facts := func(path string) (tokens []string, sections int) {
		page, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(page), `\-`, "-")
		for _, pattern := range []string{`(?:^|[\s\[(（])(--?[a-z][a-z-]*)`, `(/(?:etc|var|usr)/[a-z/._-]*[a-z])`, `(?m)^\.B ([0-3])$`} {
			for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
				tokens = append(tokens, match[1])
			}
		}
		slices.Sort(tokens)
		return tokens, strings.Count(text, "\n.SH ")
	}
	english, englishSections := facts(manPages["en"])
	if len(english) < 20 {
		t.Fatalf("too few facts in the English page: %v", english)
	}
	for lang, path := range manPages {
		tokens, sections := facts(path)
		if !slices.Equal(tokens, english) || sections != englishSections {
			t.Errorf("%s man page differs from English:\n got  %d sections %v\n want %d sections %v", lang, sections, tokens, englishSections, english)
		}
	}
}
