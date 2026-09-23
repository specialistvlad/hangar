package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// checkListenAddr accepts host:port with a numeric port a listener can
// bind. Anything else would only fail inside the service, where the operator
// sees a restart loop rather than the mistake.
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
