package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The GitHub API is spoken directly over net/http rather than by shelling out
// to the gh CLI: hangar's whole contract is that it depends on nothing the
// system happens to have installed, and gh would also drag in whatever account
// happens to be logged in there instead of the token in .env.

// A var rather than a const only so tests can point it at a stub server.
var apiBase = "https://api.github.com"

// Deadlines live on the request context rather than the client, so a slow
// 125MB download is not held to the same budget as an API call.
var httpClient = &http.Client{}

const (
	apiTimeout      = 30 * time.Second
	downloadTimeout = 15 * time.Minute
)

func (f *Fleet) apiPost(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path, nil)
	if err != nil {
		return err
	}
	return f.do(req, out, authenticated)
}

func (f *Fleet) apiGet(path string, out any) error {
	return f.get(path, out, authenticated)
}

// apiGetPublic reads an endpoint that is readable without credentials. Sending
// GH_TOKEN to one anyway makes a broken token break calls it has no business
// touching: an expired credential turns a public 200 into a 401.
func (f *Fleet) apiGetPublic(path string, out any) error {
	return f.get(path, out, anonymous)
}

func (f *Fleet) get(path string, out any, mode authMode) error {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	return f.do(req, out, mode)
}

// authMode says whether a request carries GH_TOKEN. It also decides what a 401
// or 403 means: on an authenticated call the token is the likely cause, on an
// anonymous one it cannot be, and naming it would send the operator off to
// re-mint a credential that was never involved.
type authMode bool

const (
	authenticated authMode = true
	anonymous     authMode = false
)

func (f *Fleet) do(req *http.Request, out any, mode authMode) error {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if mode == authenticated && f.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.cfg.Token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if mode == authenticated &&
		(resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) {
		return fmt.Errorf("GitHub returned %d for %s — GH_TOKEN must be a fine-grained "+
			"token whose resource owner is the org, with organization permission "+
			"\"Self-hosted runners: Read and write\" (see README)", resp.StatusCode, req.URL.Path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned %d for %s: %s", resp.StatusCode, req.URL.Path, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// runnerToken mints a short-lived token. GitHub's registration and removal
// tokens live an hour and are reusable across runners, so callers fetch one per
// scale operation rather than one per worker.
func (f *Fleet) runnerToken(kind string) (string, error) {
	var r struct {
		Token string `json:"token"`
	}
	err := f.apiPost(fmt.Sprintf("/orgs/%s/actions/runners/%s-token", f.cfg.Org, kind), &r)
	return r.Token, err
}

// CheckAuth verifies the token can actually manage this org's runners. It needs
// the same permission as minting a registration token, so a scope problem
// surfaces up front instead of halfway through provisioning a worker.
func (f *Fleet) CheckAuth() error {
	return f.apiGet(fmt.Sprintf("/orgs/%s/actions/runners?per_page=1", f.cfg.Org), nil)
}
