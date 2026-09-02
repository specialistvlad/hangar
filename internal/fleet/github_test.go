package fleet

import (
	"encoding/json"
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

const releaseBody = `{
  "tag_name": "v2.336.0",
  "body": "sha<!-- BEGIN SHA osx-arm64 -->0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef<!-- END SHA osx-arm64 -->",
  "assets": [{"name": "actions-runner-osx-arm64-2.336.0.tar.gz", "url": "http://x/a.tar.gz", "browser_download_url": "http://x/a.tar.gz"}]
}`

// The runner release lives in a public repo, so fetching it needs no
// credentials at all. Sending GH_TOKEN anyway means an expired or wrong-scoped
// token turns a call that would have succeeded into a 401 — which is what made
// `make 0` fail at a step that never needed the token in the first place.
func TestLatestReleaseSendsNoToken(t *testing.T) {
	auth := stubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/actions/runner/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(releaseBody))
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
