package mask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamPrivateKeyState(t *testing.T) {
	stream := NewStream()
	cases := []struct {
		input, visible string
		secret         bool
	}{
		{"-----BEGIN PRIVATE KEY-----", "", true},
		{"synthetic body", "", true},
		{"-----END PRIVATE KEY----- routine suffix", "routine suffix", true},
		{"routine text", "routine text", false},
	}
	for _, tc := range cases {
		got := stream.Apply(tc.input)
		if tc.secret && !strings.Contains(got.Text, "[MASKED:private-key]") {
			t.Errorf("unmasked block: %q", got.Text)
		}
		if tc.visible != "" && !strings.Contains(got.Text, tc.visible) {
			t.Errorf("lost visible suffix: %q", got.Text)
		}
		if strings.Contains(got.Text, "synthetic body") {
			t.Fatal("body remains")
		}
	}
}

func TestStreamDockerJSON(t *testing.T) {
	stream := NewStream()
	cases := []struct {
		payload, name string
		masked        bool
	}{
		{"-----BEGIN RSA PRIVATE KEY-----\n", "stdout", true},
		{"ordinary warning\n", "stderr", false},
		{"U1lOVEhFVElDLVNFQ1JFVA==\n", "stdout", true},
		{"-----END RSA PRIVATE KEY-----\n", "stdout", true},
		{"ordinary message\n", "stdout", false},
		{"-----BEGIN PRIVATE KEY-----\nsecret fragment\n-----END PRIVATE KEY-----\n", "stderr", true},
	}
	for _, tc := range cases {
		data, _ := json.Marshal(map[string]string{"log": tc.payload, "stream": tc.name, "time": "2026-10-04T00:00:00Z"})
		got := stream.Apply(string(data))
		var decoded map[string]string
		if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["time"] != "2026-10-04T00:00:00Z" || decoded["stream"] != tc.name {
			t.Fatal("envelope changed")
		}
		if tc.masked && !strings.Contains(decoded["log"], "[MASKED:private-key]") {
			t.Fatal("secret remains")
		}
		if !tc.masked && decoded["log"] != tc.payload {
			t.Fatalf("ordinary stream changed: %q", decoded["log"])
		}
	}
}

func TestStreamOrphanBody(t *testing.T) {
	stream := NewStream()
	body := "U1lOVEhFVElDLVNFQ1JFVA=="
	data, _ := json.Marshal(map[string]string{"log": body, "stream": "stdout"})
	if strings.Contains(stream.Apply(string(data)).Text, body) {
		t.Fatal("body after a seek or rotation remains")
	}
}
