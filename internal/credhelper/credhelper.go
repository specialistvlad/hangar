// Package credhelper implements Docker's credential-helper protocol, backed by
// a plain file inside the worker's own home.
//
// It exists because an empty config.json is not enough to keep credentials out
// of the macOS keychain. When credsStore is unset the Docker CLI falls back to
// a platform default, which on macOS is docker-credential-osxkeychain — and a
// keychain write from a launchd agent raises a SecurityAgent authorization
// dialog nobody can click. Every `docker login` then blocks forever, which is
// exactly what happened to four concurrent builds.
//
// Naming a helper explicitly removes that fallback. Credentials land in
// $HOME/.docker/hangar-credentials.json, which is per-worker for free because
// HOME already is.
package credhelper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Name is the credsStore value written into each worker's config.json; Docker
// resolves it by executing docker-credential-<Name> from PATH.
const Name = "hangar"

// notFound is the exact string Docker matches to tell "no stored credential"
// apart from "the helper is broken". It must reach STDOUT: the client compares
// it against the helper's stdout, and anything else — including writing it only
// to stderr — is reported as
//
//	error getting credentials - err: exit status 1, out: ``
//
// which fails the whole build instead of falling back to anonymous access.
const notFound = "credentials not found in native keychain"

// ErrNotFound signals a miss. Callers exit non-zero without adding output of
// their own, since the message has already gone to stdout.
var ErrNotFound = errors.New(notFound)

type credential struct {
	Username string `json:"Username"`
	Secret   string `json:"Secret"`
	// ServerURL is only present on the store request, not in the saved entry.
	ServerURL string `json:"ServerURL,omitempty"`
}

// Run executes one helper operation. Docker invokes the binary once per action
// with the verb as argv[1] and the payload on stdin.
func Run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: docker-credential-%s <store|get|erase|list>", Name)
	}
	body, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return err
	}

	switch args[0] {
	case "store":
		return store(body)
	case "get":
		return get(strings.TrimSpace(string(body)), stdout)
	case "erase":
		return erase(strings.TrimSpace(string(body)))
	case "list":
		return list(stdout)
	default:
		return fmt.Errorf("unknown credential action %q", args[0])
	}
}

// path is deliberately derived from HOME rather than DOCKER_CONFIG: HOME is
// what hangar isolates, so the store follows the worker automatically.
func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "hangar-credentials.json"), nil
}

func load() (map[string]credential, string, error) {
	p, err := path()
	if err != nil {
		return nil, "", err
	}
	creds := map[string]credential{}
	body, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return creds, p, nil
		}
		return nil, "", err
	}
	// A corrupt store must not wedge every future login; start clean instead.
	if err := json.Unmarshal(body, &creds); err != nil {
		return map[string]credential{}, p, nil
	}
	return creds, p, nil
}

func save(creds map[string]credential, p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	// Written 0600 and via a temp file, so a concurrent reader never sees a
	// half-written store — several workers can be logging in at once.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".creds-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func store(body []byte) error {
	var c credential
	if err := json.Unmarshal(body, &c); err != nil {
		return err
	}
	if c.ServerURL == "" {
		return fmt.Errorf("store request has no ServerURL")
	}
	creds, p, err := load()
	if err != nil {
		return err
	}
	creds[c.ServerURL] = credential{Username: c.Username, Secret: c.Secret}
	return save(creds, p)
}

func get(server string, out io.Writer) error {
	creds, _, err := load()
	if err != nil {
		return err
	}
	c, ok := creds[server]
	if !ok {
		// Stdout, not just stderr — this is what Docker actually inspects.
		// docker-credential-osxkeychain writes it to both, so match that.
		_, _ = fmt.Fprintln(out, notFound)
		return ErrNotFound
	}
	return json.NewEncoder(out).Encode(credential{Username: c.Username, Secret: c.Secret})
}

func erase(server string) error {
	creds, p, err := load()
	if err != nil {
		return err
	}
	delete(creds, server)
	return save(creds, p)
}

func list(out io.Writer) error {
	creds, _, err := load()
	if err != nil {
		return err
	}
	byServer := map[string]string{}
	for server, c := range creds {
		byServer[server] = c.Username
	}
	return json.NewEncoder(out).Encode(byServer)
}
