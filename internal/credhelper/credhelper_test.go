package credhelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The helper exists to keep the macOS keychain out of the credential path.
// These pin the protocol Docker expects; getting the miss case wrong is what
// turns a failed login into a hang.
func TestRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := `{"ServerURL":"registry.example.com","Username":"AWS","Secret":"s3cr3t"}`
	if err := Run([]string{"store"}, strings.NewReader(in), buf()); err != nil {
		t.Fatalf("store: %v", err)
	}

	var out bytes.Buffer
	if err := Run([]string{"get"}, strings.NewReader("registry.example.com"), &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	var got credential
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "AWS" || got.Secret != "s3cr3t" {
		t.Errorf("round trip lost data: %+v", got)
	}

	out.Reset()
	if err := Run([]string{"list"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), `"registry.example.com":"AWS"`) {
		t.Errorf("list = %s", out.String())
	}

	if err := Run([]string{"erase"}, strings.NewReader("registry.example.com"), buf()); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if err := Run([]string{"get"}, strings.NewReader("registry.example.com"), buf()); err == nil {
		t.Error("get after erase should fail")
	}
}

// A miss must return an error immediately. Docker treats a non-zero exit as
// "no stored credential" and moves on; anything that blocks here deadlocks the
// job, which is the failure this package was written to eliminate.
func TestMissFailsFast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := Run([]string{"get"}, strings.NewReader("absent.example.com"), buf())
	if err == nil {
		t.Fatal("expected an error for an unknown server")
	}
	if !strings.Contains(err.Error(), "credentials not found") {
		t.Errorf("Docker expects the standard miss message, got %q", err)
	}
}

// A corrupt store must not wedge every future login.
func TestCorruptStoreRecovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := Run([]string{"store"},
		strings.NewReader(`{"ServerURL":"a.example.com","Username":"u","Secret":"s"}`), buf()); err != nil {
		t.Fatal(err)
	}
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(p, "{not json"); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"store"},
		strings.NewReader(`{"ServerURL":"b.example.com","Username":"u2","Secret":"s2"}`), buf()); err != nil {
		t.Fatalf("store over a corrupt file should recover, got %v", err)
	}
}

func TestUnknownAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Run([]string{"frobnicate"}, strings.NewReader(""), buf()); err == nil {
		t.Error("unknown action should error")
	}
	if err := Run(nil, strings.NewReader(""), buf()); err == nil {
		t.Error("missing action should error")
	}
}

// small helpers to keep the tests readable
func buf() *bytes.Buffer { return &bytes.Buffer{} }

func writeFile(p, body string) error { return os.WriteFile(p, []byte(body), 0o600) }

// A miss must put the not-found string on STDOUT. Docker compares the helper's
// stdout against that exact text to distinguish "no credential for this
// registry" from "this helper is broken"; on stderr alone it reports
// `error getting credentials ... out: ``` and fails the build. This exact
// mistake broke two builds at the docker-build step.
func TestMissWritesNotFoundToStdout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout bytes.Buffer
	err := Run([]string{"get"}, strings.NewReader("absent.example.com"), &stdout)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "credentials not found in native keychain" {
		t.Errorf("stdout = %q, want the exact not-found string Docker matches on", got)
	}
}
