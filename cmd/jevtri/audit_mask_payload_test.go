package main

import (
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
