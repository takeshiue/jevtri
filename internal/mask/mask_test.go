package mask

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MK-01: the positive and negative examples from the specification.
func TestDefaultRules(t *testing.T) {
	masker, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"-----BEGIN RSA PRIVATE KEY----- MIIEow\nabc\n-----END RSA PRIVATE KEY----- tail", "[MASKED:private-key] tail"},
		{"Authorization: Bearer abc.def.ghi", "Authorization: Bearer [MASKED:authorization]"},
		{"proxy-authorization=Basic dXNlcjpwYXNz", "proxy-authorization=Basic [MASKED:authorization]"},
		{"password=S3cret! next", "password=[MASKED:secret] next"},
		{"DB_PASSWORD: hunter2", "DB_PASSWORD: [MASKED:secret]"},
		{`{"api_key":"abc123"}`, `{"api_key":"[MASKED:secret]"}`},
		{"client_secret = xyz", "client_secret = [MASKED:secret]"},
		{"mysql://app:pw123@db01:3306/shop", "mysql://app:[MASKED:url-credential]@db01:3306/shop"},
		{"GET /api?user=1&access_token=zzz HTTP/1.1", "GET /api?user=1&access_token=[MASKED:query-token] HTTP/1.1"},
		{"key AKIAABCDEFGHIJKLMNOP used", "key [MASKED:known-token] used"},
		{"mail to alice@example.com failed", "mail to [MASKED:email]@example.com failed"},
		{"Cookie: sid=abc; theme=dark", "Cookie: [MASKED:cookie]"},
		{"card 4111 1111 1111 1111 ok", "card [MASKED:card] ok"},
		{"5500-0000-0000-0004", "[MASKED:card]"},
		// Must stay as they are.
		{"Access denied for user 'root'@'localhost' (using password: YES)", "Access denied for user 'root'@'localhost' (using password: YES)"},
		{"2026-09-11 02:03:30 status installed passwd:amd64 1:4.13", "2026-09-11 02:03:30 status installed passwd:amd64 1:4.13"},
		{"sudo: deploy : TTY=pts/0 ; PWD=/home/deploy ; USER=root", "sudo: deploy : TTY=pts/0 ; PWD=/home/deploy ; USER=root"},
		{"Failed password for invalid user admin from 127.0.0.1", "Failed password for invalid user admin from 127.0.0.1"},
		{"Started systemd-coredump@0-13813-0.service", "Started systemd-coredump@0-13813-0.service"},
		{"ts 1790559514624557 id 0000000000000000", "ts 1790559514624557 id 0000000000000000"},
		{"https://example.com/a", "https://example.com/a"},
	}
	for _, c := range cases {
		if got := masker.Apply(c.in).Text; got != c.want {
			t.Errorf("Apply(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// MK-02: extra patterns from the configuration.
func TestExtraPatterns(t *testing.T) {
	masker, err := New([]string{`order-\d+`, `\b\d{1,3}(\.\d{1,3}){3}\b`})
	if err != nil {
		t.Fatal(err)
	}
	result := masker.Apply("order-123 from 192.0.2.10 failed")
	if result.Text != "[MASKED:custom] from [MASKED:custom] failed" || result.Counts["custom"] != 2 {
		t.Errorf("got %q %v", result.Text, result.Counts)
	}
	if _, err := New([]string{"("}); err == nil {
		t.Error("expected an error for an invalid pattern")
	}
}

// The real samples must only be masked where intended (e-mail addresses).
func TestNoFalsePositivesInSamples(t *testing.T) {
	masker, _ := New(nil)
	root := filepath.Join("..", "..", "testdata", "logs")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, ".md") {
			return err
		}
		handle, err := os.Open(path)
		if err != nil {
			return err
		}
		defer handle.Close()
		scanner := bufio.NewScanner(handle)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			result := masker.Apply(scanner.Text())
			for kind, count := range result.Counts {
				if kind != "email" && count > 0 {
					t.Errorf("%s: unexpected %s mask in %q", path, kind, scanner.Text())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
