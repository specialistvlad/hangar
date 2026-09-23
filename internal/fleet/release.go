// Package-level docs live in github.go: this file is the runner-release half of
// the same GitHub client — finding the newest build and caching its tarball.
package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Release describes the runner build hangar should be installing.
type Release struct {
	Version  string // e.g. "2.336.0"
	Platform string // e.g. "linux-x64", as runnerPlatform spells it
	URL      string
	SHA256   string
}

// runnerPlatform names the actions/runner build for goos/goarch the way its
// release assets and checksum markers spell it.
func runnerPlatform(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "osx-arm64", nil
	case "darwin/amd64":
		return "osx-x64", nil
	case "linux/amd64":
		return "linux-x64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	case "linux/arm":
		return "linux-arm", nil
	}
	return "", fmt.Errorf("actions/runner publishes no build hangar can drive for %s/%s", goos, goarch)
}

// hostPlatform is runnerPlatform for the machine hangar is running on.
func hostPlatform() (string, error) { return runnerPlatform(runtime.GOOS, machineArch()) }

// assetName is the exact release asset for a platform and version. Matched in
// full rather than by substring: releases also carry trimmed variants of the
// same build, and picking one of those by accident installs a runner without
// its bundled node.
func assetName(platform, version string) string {
	return fmt.Sprintf("actions-runner-%s-%s.tar.gz", platform, version)
}

// shaFor extracts a platform's checksum from the release notes.
func shaFor(body, platform string) string {
	q := regexp.QuoteMeta(platform)
	re := regexp.MustCompile(`<!-- BEGIN SHA ` + q + ` -->([a-f0-9]{64})<!-- END SHA ` + q + ` -->`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// LatestRelease asks GitHub for the newest runner and the checksum published
// alongside it. The checksum is embedded in the release notes rather than in a
// separate asset, which is why the body is scraped.
func (f *Fleet) LatestRelease() (*Release, error) {
	platform, err := hostPlatform()
	if err != nil {
		return nil, err
	}
	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	// actions/runner is a public repo: this is readable without credentials.
	if err := f.apiGetPublic("/repos/actions/runner/releases/latest", &r); err != nil {
		return nil, err
	}

	rel := &Release{Version: strings.TrimPrefix(r.Tag, "v"), Platform: platform}
	want := assetName(platform, rel.Version)
	for _, a := range r.Assets {
		if a.Name == want {
			rel.URL = a.URL
			break
		}
	}
	if rel.URL == "" {
		return nil, fmt.Errorf("release %s has no %s asset", r.Tag, want)
	}
	rel.SHA256 = shaFor(r.Body, platform)
	return rel, nil
}

func (f *Fleet) tarballPath(rel *Release) string {
	return filepath.Join(f.cfg.CacheDir(), assetName(rel.Platform, rel.Version))
}

// EnsureTarball downloads the release if it is not already cached, verifying
// the checksum before the file is moved into place. A tarball that fails
// verification is discarded rather than cached, so a corrupt download cannot
// poison every future worker.
func (f *Fleet) EnsureTarball(rel *Release, progress func(string)) (string, error) {
	dst := f.tarballPath(rel)
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
	platform, err := hostPlatform()
	if err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(f.cfg.CacheDir(), assetName(platform, "*")))
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
