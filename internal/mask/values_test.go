package mask

import (
	"strings"
	"testing"
)

func TestQuotedValuesAreFullyMasked(t *testing.T) {
	masker, err := New([]string{`CUSTOMER-[0-9]+`})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ input, want string }{
		{`password='secret value,tail' status=failed`, `password='[MASKED:secret]' status=failed`},
		{`{"password":"one two,three","status":"failed"}`, `{"password":"[MASKED:secret]","status":"failed"}`},
		{`{"password":"one\"two\\three","status":"failed"}`, `{"password":"[MASKED:secret]","status":"failed"}`},
		{`{"password": "a b; c"}`, `{"password": "[MASKED:secret]"}`},
		{`{'password':'a b'}`, `{'password':'[MASKED:secret]'}`},
		{`{\"password\":\"one two,three\",\"status\":\"failed\"}`, `{\"password\":\"[MASKED:secret]\",\"status\":\"failed\"}`},
		{`{\"password\":\"one\\\"two\",\"status\":\"failed\"}`, `{\"password\":\"[MASKED:secret]\",\"status\":\"failed\"}`},
		{`password='unterminated secret`, `password='[MASKED:secret]`},
		{"password='unterminated secret\nstatus=failed", "password='[MASKED:secret]\nstatus=failed"},
		{`password='one\'two'`, `password='[MASKED:secret]'`},
		{`password=""`, `password=""`},
		{`password='YES'`, `password='YES'`},
		{`customer CUSTOMER-123`, `customer [MASKED:custom]`},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			result := masker.Apply(c.input)
			if result.Text != c.want {
				t.Fatalf("got %q; want %q", result.Text, c.want)
			}
			if again := masker.Apply(result.Text).Text; again != c.want {
				t.Fatalf("masking is not idempotent: %q", again)
			}
		})
	}
}

func TestAuthorizationMasksWholeCredential(t *testing.T) {
	masker, _ := New(nil)
	cases := []struct{ input, want string }{
		{`Authorization: Digest username="alice", realm="shop", nonce="nonce-value", response="response-value"`, `Authorization: Digest [MASKED:authorization]`},
		{"Authorization: Digest username=alice, response=hash\nHTTP/1.1 401 Unauthorized", "Authorization: Digest [MASKED:authorization]\nHTTP/1.1 401 Unauthorized"},
		{`{"Authorization":"Digest username=alice, nonce=value, response=hash","status":401}`, `{"Authorization":"[MASKED:authorization]","status":401}`},
		{`Authorization: Bearer "a b,c" status=failed`, `Authorization: Bearer "[MASKED:authorization]" status=failed`},
		{`proxy-authorization=Basic 'a b'`, `proxy-authorization=Basic '[MASKED:authorization]'`},
	}
	for _, c := range cases {
		result := masker.Apply(c.input)
		if result.Text != c.want || result.Counts["authorization"] != 1 {
			t.Errorf("got %q %v; want %q", result.Text, result.Counts, c.want)
		}
	}
}

func FuzzSecretValueNeverSurvives(f *testing.F) {
	for _, value := range []string{"one two,three", "a\"b\\c", "plain", "x\ny", "[MASKED:secret]leak"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if value == "" || strings.EqualFold(value, "YES") || strings.EqualFold(value, "NO") || strings.Trim(value, "*") == "" {
			return
		}
		// Escaping the value keeps arbitrary input within one complete quoted field.
		escaped := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r").Replace(value)
		masker, _ := New(nil)
		result := masker.Apply(`{"password":"` + escaped + `","status":"failed"}`)
		if result.Text != `{"password":"[MASKED:secret]","status":"failed"}` {
			t.Fatalf("unexpected result %q", result.Text)
		}
	})
}
