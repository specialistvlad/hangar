package config

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultJobTimeoutMinutes is the limit a worker applies when .env does not
// set JOB_TIMEOUT_MINUTES. GitHub's own default is six hours, and a job that
// hangs holds its worker for all of it; half an hour covers a typical build
// while still freeing a stuck worker the same afternoon.
const DefaultJobTimeoutMinutes = 30

// maxJobTimeoutMinutes is GitHub's ceiling for a job on a self-hosted runner,
// five days. A longer limit could never be reached, so it is almost certainly
// a typo — an hours value read as minutes, or a stray digit.
const maxJobTimeoutMinutes = 5 * 24 * 60

// parseJobTimeout reads JOB_TIMEOUT_MINUTES: unset is the default, 0 turns the
// limit off, and anything that is not a whole number of minutes within
// GitHub's ceiling is refused. A value hangar cannot read must not quietly
// fall back to a default: the operator who wrote "90m" meant ninety minutes,
// and getting thirty would kill their builds.
func parseJobTimeout(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultJobTimeoutMinutes, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > maxJobTimeoutMinutes {
		return 0, fmt.Errorf("JOB_TIMEOUT_MINUTES must be a whole number of minutes from 0 (no limit) to %d, got %q",
			maxJobTimeoutMinutes, raw)
	}
	return n, nil
}
