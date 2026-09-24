package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/specialistvlad/hangar/internal/fleet"
)

// jobCommand runs the two commands the runner reaches through a worker's job
// hooks rather than an operator: `job-hook started|completed <worker>`, which
// the hook scripts exec, and `job-watch <worker> <pid> <deadline> <limit>`,
// the watchdog the started hook detaches. See internal/fleet/jobhook.go.
func jobCommand(f *fleet.Fleet, args []string) error {
	nums := func(ss []string) ([]int64, error) {
		out := make([]int64, len(ss))
		for i, s := range ss {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", args[0], s)
			}
			out[i] = n
		}
		return out, nil
	}
	switch {
	case args[0] == "job-hook" && len(args) == 3:
		v, err := nums(args[2:])
		if err != nil {
			return err
		}
		switch args[1] {
		case "started":
			return f.JobStarted(int(v[0]), os.Stdout)
		case "completed":
			return f.JobCompleted(int(v[0]), os.Stdout)
		}
	case args[0] == "job-watch" && len(args) == 5:
		v, err := nums(args[1:])
		if err != nil {
			return err
		}
		return f.Watch(int(v[0]), int(v[1]), time.Unix(v[2], 0), int(v[3]), os.Stdout)
	}
	return fmt.Errorf("%s: unexpected arguments %q — this command is run by a worker's job hooks, not by hand", args[0], args[1:])
}
