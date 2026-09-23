package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

// checkHostResolves must look up only a named host, and report a lookup
// failure as its own error. The lookup is injected so the case where a name
// does not resolve is exercised without touching a real resolver.
func TestCheckHostResolves(t *testing.T) {
	errNoSuchHost := errors.New("no such host")
	fail := func(context.Context, string) ([]string, error) { return nil, errNoSuchHost }
	ok := func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }

	for _, tc := range []struct {
		name       string
		addr       string
		lookup     func(context.Context, string) ([]string, error)
		wantErr    bool
		wantLookup bool
	}{
		{"empty host means every interface, no lookup needed", ":9151", fail, false, false},
		{"an IPv4 host needs no lookup", "127.0.0.1:9151", fail, false, false},
		{"an IPv6 host needs no lookup", "[::1]:9151", fail, false, false},
		{"a malformed address is left to checkListenAddr", "not-an-addr", fail, false, false},
		{"a name that resolves passes", "metrics.internal:9151", ok, false, true},
		{"a name that does not resolve is reported", "metrics.internal:9151", fail, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			lookup := func(ctx context.Context, host string) ([]string, error) {
				called = true
				return tc.lookup(ctx, host)
			}
			err := checkHostResolves(tc.addr, time.Second, lookup)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if called != tc.wantLookup {
				t.Errorf("lookup called = %v, want %v", called, tc.wantLookup)
			}
		})
	}
}
