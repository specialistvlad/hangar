package config

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// checkListenAddr checks addr's shape: host:port, with a numeric port from 1
// to 65535. It runs for every subcommand through config.Load, so it stops
// there rather than resolving the host — a name that does not exist is still
// only caught where a working METRICS_ADDR actually matters, by
// CheckHostResolves.
func checkListenAddr(addr string) error {
	if strings.ContainsAny(addr, " \t#") {
		return fmt.Errorf("%q must be host:port with nothing after it", addr)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q must be host:port, e.g. 127.0.0.1:9151", addr)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%q needs a port from 1 to 65535", addr)
	}
	return nil
}

// MetricsURL turns a listen address into the URL an operator can open.
// net.SplitHostPort allows an empty host to mean "every interface" — METRICS_
// ADDR=:9151 is valid and checkListenAddr accepts it — but "http://:9151/…" is
// not a host a browser, curl or a Prometheus scrape_config accepts as
// written, so an empty or unspecified host is displayed as 127.0.0.1 instead,
// the same substitution WaitServing already makes to poll the service.
func MetricsURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/metrics"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/metrics"
}

// CheckHostResolves resolves addr's host once, with a short deadline, when it
// is a name rather than an IP or the empty host that means every interface —
// the two cases checkListenAddr already lets through untouched. A name that
// does not exist or cannot be reached is reported here, in seconds, rather
// than only after `hangar metrics start` has written the service and
// WaitServing's own poll has spent its whole timeout finding the same thing
// out.
func CheckHostResolves(addr string, timeout time.Duration) error {
	return checkHostResolves(addr, timeout, net.DefaultResolver.LookupHost)
}

// checkHostResolves takes the lookup as a parameter so a test can supply one
// that never touches the network.
func checkHostResolves(addr string, timeout time.Duration, lookup func(context.Context, string) ([]string, error)) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" || net.ParseIP(host) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := lookup(ctx, host); err != nil {
		return fmt.Errorf("%q does not resolve: %w", host, err)
	}
	return nil
}
