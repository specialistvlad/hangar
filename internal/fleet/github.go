package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The GitHub API is spoken directly over net/http rather than by shelling out
// to the gh CLI: hangar's whole contract is that it depends on nothing the
// system happens to have installed, and gh would also drag in whatever account
// happens to be logged in there instead of the token in .env.

const apiBase = "https://api.github.com"

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
	return f.do(req, out)
}

func (f *Fleet) apiGet(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	return f.do(req, out)
}

func (f *Fleet) do(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.cfg.Token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
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

// Release describes the runner build hangar should be installing.
type Release struct {
	Version string // e.g. "2.336.0"
	URL     string
	SHA256  string
}

var shaRe = regexp.MustCompile(`<!-- BEGIN SHA osx-arm64 -->([a-f0-9]{64})<!-- END SHA osx-arm64 -->`)

// LatestRelease asks GitHub for the newest runner and the checksum published
// alongside it. The checksum is embedded in the release notes rather than in a
// separate asset, which is why the body is scraped.
func (f *Fleet) LatestRelease() (*Release, error) {
	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := f.apiGet("/repos/actions/runner/releases/latest", &r); err != nil {
		return nil, err
	}

	rel := &Release{Version: strings.TrimPrefix(r.Tag, "v")}
	for _, a := range r.Assets {
		if strings.Contains(a.Name, "osx-arm64") && strings.HasSuffix(a.Name, ".tar.gz") {
			rel.URL = a.URL
			break
		}
	}
	if rel.URL == "" {
		return nil, fmt.Errorf("release %s has no osx-arm64 asset", r.Tag)
	}
	if m := shaRe.FindStringSubmatch(r.Body); m != nil {
		rel.SHA256 = m[1]
	}
	return rel, nil
}

func (f *Fleet) tarballPath(version string) string {
	return filepath.Join(f.cfg.CacheDir(), fmt.Sprintf("actions-runner-osx-arm64-%s.tar.gz", version))
}

// EnsureTarball downloads the release if it is not already cached, verifying
// the checksum before the file is moved into place. A tarball that fails
// verification is discarded rather than cached, so a corrupt download cannot
// poison every future worker.
func (f *Fleet) EnsureTarball(rel *Release, progress func(string)) (string, error) {
	dst := f.tarballPath(rel.Version)
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		if rel.SHA256 == "" {
			return dst, nil
		}
		sum, err := fileSHA256(dst)
		if err == nil && sum == rel.SHA256 {
			return dst, nil
		}
		progress(fmt.Sprintf("cached %s failed checksum, re-downloading", filepath.Base(dst)))
	}

	if err := os.MkdirAll(f.cfg.CacheDir(), 0o755); err != nil {
		return "", err
	}
	progress(fmt.Sprintf("downloading runner %s", rel.Version))
	// The checksum is scraped out of the release notes, so a formatting change
	// upstream makes it vanish rather than error. Downloading unverified is still
	// better than refusing to work, but it must not happen silently.
	if rel.SHA256 == "" {
		progress("WARNING: no checksum published for this release — cannot verify the download")
	}

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", rel.URL, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(f.cfg.CacheDir(), "download-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	if got := hex.EncodeToString(h.Sum(nil)); rel.SHA256 != "" && got != rel.SHA256 {
		return "", fmt.Errorf("checksum mismatch for runner %s: got %s want %s", rel.Version, got, rel.SHA256)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	progress(fmt.Sprintf("runner %s cached", rel.Version))
	return dst, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CachedTarball returns the newest runner tarball already on disk, so scaling
// up works offline once anything has been downloaded.
func (f *Fleet) CachedTarball() (string, error) {
	matches, _ := filepath.Glob(filepath.Join(f.cfg.CacheDir(), "actions-runner-osx-arm64-*.tar.gz"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no runner tarball cached — run `make update` first")
	}
	newest, newestMod := "", time.Time{}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.ModTime().After(newestMod) {
			newest, newestMod = m, fi.ModTime()
		}
	}
	return newest, nil
}
