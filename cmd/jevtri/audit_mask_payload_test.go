package main

import (
	"encoding/json"
	"github.com/takeshiue/jevtri/internal/jev"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditQuotedAndDigestSecretsAbsentFromSentPayloadAndRecord(t *testing.T) {
	f := newFixture(t, "mask = CUSTOMER-[0-9]+\n")
	text := "2026-09-28 03:02:30 ERROR password='synthetic secret tail' CUSTOMER-12345\n" +
		"2026-09-28 03:02:31 ERROR Authorization: Digest username=alice, nonce=synthetic-nonce, response=synthetic-response\n"
	if err := os.WriteFile(filepath.Join(f.dir, "app.log"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("-t", "03:02"); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	var payload strings.Builder
	for _, log := range f.asker.query.Logs {
		payload.WriteString(log.Text)
	}
	recorded, err := os.ReadFile(filepath.Join(f.dir, "state", "sent.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic secret tail", "CUSTOMER-12345", "synthetic-nonce", "synthetic-response", "username=alice"} {
		for _, output := range []string{payload.String(), string(recorded), f.stdout.String(), f.stderr.String()} {
			if strings.Contains(output, secret) {
				t.Errorf("synthetic secret survived: %q", secret)
			}
		}
	}
	for _, marker := range []string{"[MASKED:secret]", "[MASKED:authorization]", "[MASKED:custom]"} {
		if !strings.Contains(payload.String(), marker) || !strings.Contains(string(recorded), marker) {
			t.Errorf("missing protection %s", marker)
		}
	}
}

func TestAuditSplitPrivateKeyAbsentFromRequestAndRecord(t *testing.T) {
	f := newFixture(t, "")
	config, err := os.ReadFile(f.conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.conf, []byte(strings.Replace(string(config), "time_format = iso-space", "time_format = docker-json", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	body := "U1lOVEhFVElDLVNFQ1JFVC1CT0RZLUNBTkFSWQ=="
	var data strings.Builder
	for _, part := range []string{"-----BEGIN PRIVATE KEY-----", body, "-----END PRIVATE KEY-----"} {
		line, _ := json.Marshal(map[string]string{"log": part + "\n", "stream": "stdout", "time": "2026-09-28T03:02:30+09:00"})
		data.Write(line)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(f.dir, "app.log"), []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if code := f.run("-t", "03:02"); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, f.stderr.String())
	}
	request, err := json.Marshal(jev.BuildRequest(f.asker.query))
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(filepath.Join(f.dir, "state", "sent.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(request), string(recorded), f.stdout.String(), f.stderr.String()} {
		if strings.Contains(output, body) {
			t.Fatal("split private body survived in request, audit or display")
		}
	}
	if !strings.Contains(string(request), "[MASKED:private-key]") || !strings.Contains(string(recorded), "[MASKED:private-key]") {
		t.Fatal("no evidence app private entries were processed")
	}
}
