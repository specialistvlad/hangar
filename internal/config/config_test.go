package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	body := `
# a comment
GH_TOKEN=ghp_secret
GH_ORG = Acme
RUNNER_GROUP="quoted macs"
RUNNER_LABELS=
EMPTY_LINE_ABOVE=yes
not a pair
`
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	env, err := parseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GH_TOKEN":         "ghp_secret",
		"GH_ORG":           "Acme",
		"RUNNER_GROUP":     "quoted macs",
		"RUNNER_LABELS":    "",
		"EMPTY_LINE_ABOVE": "yes",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["not a pair"]; ok {
		t.Error("a line without = should be skipped")
	}
}

func TestParseEnvFileMissing(t *testing.T) {
	if _, err := parseEnvFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error naming the missing file")
	}
}

func TestParseCount(t *testing.T) {
	for _, s := range []string{"0", "1", "32"} {
		if _, err := ParseCount(s); err != nil {
			t.Errorf("ParseCount(%q) errored: %v", s, err)
		}
	}
	for _, s := range []string{"-1", "33", "abc", "", "3.5"} {
		if _, err := ParseCount(s); err == nil {
			t.Errorf("ParseCount(%q) should have failed", s)
		}
	}
}

// A .env that exists but is half-filled must load, so `make update` still works
// before a token has been pasted in. Only the GitHub-touching commands fail.
func TestRequireGitHub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GH_ORG=Acme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load of a partial .env should succeed, got %v", err)
	}
	if err := c.RequireGitHub(); err == nil {
		t.Fatal("RequireGitHub should report the missing GH_TOKEN")
	}
	if c.NamePrefix == "" {
		t.Error("NamePrefix should fall back to a hostname-derived default")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" .npm , .cache/go-build ,, ")
	if len(got) != 2 || got[0] != ".npm" || got[1] != ".cache/go-build" {
		t.Errorf("got %#v, want [.npm .cache/go-build]", got)
	}
	if got := splitList(""); got != nil {
		t.Errorf("empty setting should yield nil, got %#v", got)
	}
}
