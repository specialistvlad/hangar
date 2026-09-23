package config

import "testing"

// MetricsURL must turn every address checkListenAddr accepts into a URL an
// operator can actually open — an empty or unspecified host substituted for
// 127.0.0.1, everything else passed through.
func TestMetricsURL(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{":9151", "http://127.0.0.1:9151/metrics"},
		{"0.0.0.0:9151", "http://127.0.0.1:9151/metrics"},
		{"[::]:9151", "http://127.0.0.1:9151/metrics"},
		{"127.0.0.1:9151", "http://127.0.0.1:9151/metrics"},
		{"[::1]:9151", "http://[::1]:9151/metrics"},
		{"metrics.internal:9151", "http://metrics.internal:9151/metrics"},
	} {
		if got := MetricsURL(tc.addr); got != tc.want {
			t.Errorf("MetricsURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// A malformed address has no host:port to split — checkListenAddr would have
// refused it before Load ever returns a Config carrying it — so MetricsURL
// falls back to printing it verbatim rather than panicking or hiding it.
func TestMetricsURLMalformed(t *testing.T) {
	if got, want := MetricsURL("not-an-addr"), "http://not-an-addr/metrics"; got != want {
		t.Errorf("MetricsURL(%q) = %q, want %q", "not-an-addr", got, want)
	}
}
