package fleet

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// stubAPI points apiBase at a test server and records the Authorization header
// of the last request it served.
func stubAPI(t *testing.T, handler http.HandlerFunc) *string {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = prev })
	return &auth
}

// releaseBody is a latest-release response carrying the host's own build, its
// trimmed variant, and another platform's build, each with its own checksum.
// Only the host's exact asset and checksum may be picked.
func releaseBody(t *testing.T) (body, platform string) {
	t.Helper()
	platform, err := hostPlatform()
	if err != nil {
		t.Skipf("no runner build for this platform: %v", err)
	}
	other := "win-x64"
	sha := func(c string) string { return strings.Repeat(c, 64) }
	notes := fmt.Sprintf("<!-- BEGIN SHA %s -->%s<!-- END SHA %s -->", other, sha("b"), other) +
		fmt.Sprintf("<!-- BEGIN SHA %s -->%s<!-- END SHA %s -->", platform, sha("a"), platform)
	asset := func(name string) map[string]string {
		return map[string]string{"name": name, "browser_download_url": "http://x/" + name}
	}
	b, _ := json.Marshal(map[string]any{
		"tag_name": "v2.336.0",
		"body":     notes,
		"assets": []map[string]string{
			asset("actions-runner-" + platform + "-2.336.0-noexternals.tar.gz"),
			asset("actions-runner-" + other + "-2.336.0.tar.gz"),
			asset("actions-runner-" + platform + "-2.336.0.tar.gz"),
		},
	})
	return string(b), platform
}

// The runner release lives in a public repo, so fetching it needs no
// credentials at all. Sending GH_TOKEN anyway means an expired or wrong-scoped
// token turns a call that would have succeeded into a 401 — which is what made
// `make 0` fail at a step that never needed the token in the first place.
func TestLatestReleaseSendsNoToken(t *testing.T) {
	body, platform := releaseBody(t)
	auth := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/actions/runner/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})
	f := New(&config.Config{Root: "/r", Org: "acme", Token: "ghp_expired"})

	rel, err := f.LatestRelease()
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if *auth != "" {
		t.Errorf("public release endpoint must be called anonymously, sent %q", *auth)
	}
	if rel.Version != "2.336.0" {
		t.Errorf("version = %q, want 2.336.0", rel.Version)
	}
	// The exact build for this host, not its trimmed variant or another
	// platform's: a substring match picks whichever the API lists first.
	if want := "http://x/actions-runner-" + platform + "-2.336.0.tar.gz"; rel.URL != want {
		t.Errorf("URL = %q, want %q", rel.URL, want)
	}
	if rel.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("SHA256 = %q, want this platform's checksum", rel.SHA256)
	}
}

func TestRunnerPlatform(t *testing.T) {
	for in, want := range map[string]string{
		"darwin/arm64": "osx-arm64",
		"darwin/amd64": "osx-x64",
		"linux/amd64":  "linux-x64",
		"linux/arm64":  "linux-arm64",
		"linux/arm":    "linux-arm",
	} {
		goos, goarch, _ := strings.Cut(in, "/")
		got, err := runnerPlatform(goos, goarch)
		if err != nil || got != want {
			t.Errorf("runnerPlatform(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := runnerPlatform("windows", "amd64"); err == nil {
		t.Error("an unsupported platform must be an error, not an empty asset name")
	}
}

// A dead token must still be reported as a dead token when the endpoint really
// does need one, complete with the advice for fixing it.
func TestCheckAuthSendsTokenAndExplains401(t *testing.T) {
	auth := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	f := New(&config.Config{Root: "/r", Org: "acme", Token: "ghp_expired"})

	err := f.CheckAuth()
	if *auth != "Bearer ghp_expired" {
		t.Errorf("Authorization = %q, want the configured token", *auth)
	}
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("401 on an org endpoint must name GH_TOKEN, got: %v", err)
	}
}

// Rate limiting is what an anonymous caller actually hits, and it has nothing
// to do with GH_TOKEN — pointing at the token would send the operator to
// re-mint a credential that was never the problem.
func TestPublicEndpointErrorDoesNotBlameTheToken(t *testing.T) {
	stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "API rate limit exceeded"})
	})
	f := New(&config.Config{Root: "/r", Org: "acme", Token: "ghp_expired"})

	_, err := f.LatestRelease()
	if err == nil {
		t.Fatal("want an error for HTTP 403")
	}
	if strings.Contains(err.Error(), "GH_TOKEN") {
		t.Errorf("anonymous call must not blame GH_TOKEN, got: %v", err)
	}
}
