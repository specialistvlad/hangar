package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The job time limit is enforced from inside the job, through the runner's own
// job hooks, rather than by signaling the runner from outside.
//
// Signaling Runner.Worker would be simpler, and it does fail the job — but the
// runner treats that as its own shutdown: it skips every step still to come,
// post steps and "Stop containers" included (StepsRunner does not even
// evaluate their conditions once RunnerShutdownToken fires), so a job's
// service containers, logins and checkouts would be left behind on a machine
// that runs the next job straight after. Stopping the step that is running
// instead lets the runner finish the job the ordinary way: the step fails,
// the steps after it are skipped, and the post steps, the container clean-up
// and the job-completed hook all run.
//
// So every worker's .env names two hooks. The job-started hook arms a
// watchdog — `hangar job-watch`, detached from the job — that stops the
// running step once the limit passes. The job-completed hook disarms it and,
// when the limit did pass, says so in the job's annotations and exits 1, so
// the job fails even when the step it stopped had continue-on-error. Both call
// this binary, which reads JOB_TIMEOUT_MINUTES when each job starts: changing
// the limit needs no restart, only hooks that exist, which provisioning and
// scale's refresh write.

// Hook script names in the worker directory. The runner picks an interpreter
// by extension, so they must end in .sh.
const (
	startedHook   = "hangar-job-started.sh"
	completedHook = "hangar-job-completed.sh"
)

// jobStateDir holds worker n's state for the job it is running: the
// watchdog's pid and, once the limit passes, the expired marker. A worker runs
// one job at a time, so one directory per worker is enough.
func (f *Fleet) jobStateDir(n int) string { return filepath.Join(f.cfg.WorkerDir(n), ".hangar-job") }

// hookEnv is the lines the runner needs to find worker n's hooks. The runner
// reads them from .env at service start, like the rest of workerEnv.
func (f *Fleet) hookEnv(n int) []string {
	return []string{
		"ACTIONS_RUNNER_HOOK_JOB_STARTED=" + filepath.Join(f.cfg.WorkerDir(n), startedHook),
		"ACTIONS_RUNNER_HOOK_JOB_COMPLETED=" + filepath.Join(f.cfg.WorkerDir(n), completedHook),
	}
}

// hookScripts returns worker n's hook scripts by file name. They pin
// HANGAR_ROOT rather than let hangar find its root from the working directory:
// a hook runs inside the job's checkout, which may hold a go.mod of its own.
func (f *Fleet) hookScripts(n int) (map[string]string, error) {
	bin, err := selfPath()
	if err != nil {
		return nil, err
	}
	script := func(stage string) string {
		return fmt.Sprintf("#!/bin/sh\n# Written by hangar: worker %d's job-%s hook. Scaling rewrites it; edits here are lost.\n"+
			"HANGAR_ROOT=%s exec %s job-hook %s %d\n", n, stage, shQuote(f.cfg.Root), shQuote(bin), stage, n)
	}
	return map[string]string{startedHook: script("started"), completedHook: script("completed")}, nil
}

// minutes spells out a limit for the job log: "1 minute", "30 minutes".
func minutes(n int) string {
	if n == 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}

// shQuote single-quotes s for sh, so any path survives as one word.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// JobStarted is worker n's job-started hook. It clears whatever an earlier job
// left, then, unless the limit is off, arms the watchdog. The deadline counts
// from when the job's Runner.Worker started, not from the hook: the runner
// downloads the job's actions before it runs any hook.
func (f *Fleet) JobStarted(n int, out io.Writer) error {
	table, err := readProcTable()
	if err != nil {
		return err
	}
	return f.jobStarted(n, out, table, os.Getpid())
}

// jobStarted is JobStarted against a given process table and hook pid, which
// is what lets it be tested without being run by a real Runner.Worker — or,
// in CI on a hosted runner, by one the test did not mean to find.
func (f *Fleet) jobStarted(n int, out io.Writer, table procTable, self int) error {
	dir := f.jobStateDir(n)
	f.stopWatchdog(n, table)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	limit := f.cfg.JobTimeoutMinutes
	if limit == 0 {
		_, _ = fmt.Fprintln(out, "hangar: no job time limit on this runner (JOB_TIMEOUT_MINUTES=0)")
		return nil
	}
	worker, ok := table.ancestor(self, runnerWorkerName)
	if !ok {
		return fmt.Errorf("cannot find the %s this hook runs under, so the job time limit cannot be enforced", runnerWorkerName)
	}
	deadline := time.Now().Add(-table[worker].Elapsed).Add(time.Duration(limit) * time.Minute)
	if err := f.spawnWatchdog(n, worker, deadline, limit); err != nil {
		return fmt.Errorf("arming the job time limit: %w", err)
	}
	_, _ = fmt.Fprintf(out, "hangar: this runner stops a job %s after it starts (JOB_TIMEOUT_MINUTES); "+
		"this one must finish by %s\n", minutes(limit), deadline.UTC().Format(time.RFC3339))
	return nil
}

// spawnWatchdog starts `hangar job-watch` in a session of its own, so it
// outlives the hook, with its output in the state directory. Its stdio must
// not be the hook's: the runner waits for a step's output to close, and a
// watchdog holding it would hold the job-started step open for the whole job.
func (f *Fleet) spawnWatchdog(n, worker int, deadline time.Time, limit int) error {
	dir := f.jobStateDir(n)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	bin, err := selfPath()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "watchdog.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	// Background, not a deadline: the watchdog's lifetime is the job's, which
	// it measures itself.
	cmd := exec.CommandContext(context.Background(), bin, "job-watch", strconv.Itoa(n), strconv.Itoa(worker),
		strconv.FormatInt(deadline.Unix(), 10), strconv.Itoa(limit))
	cmd.Env = append(os.Environ(), "HANGAR_ROOT="+f.cfg.Root)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return os.WriteFile(filepath.Join(dir, "watchdog.pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// JobCompleted is worker n's job-completed hook. It stops the watchdog and,
// when the limit passed, names it in the job's annotations and fails.
func (f *Fleet) JobCompleted(n int, out io.Writer) error {
	table, err := readProcTable()
	if err != nil {
		return err
	}
	f.stopWatchdog(n, table)
	body, err := os.ReadFile(filepath.Join(f.jobStateDir(n), expiredMarker))
	if rmErr := os.RemoveAll(f.jobStateDir(n)); rmErr != nil {
		return rmErr
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	limit := strings.TrimSpace(string(body))
	_, _ = fmt.Fprintf(out, "::error title=Job time limit::This job ran past the %s-minute limit this runner sets "+
		"(JOB_TIMEOUT_MINUTES in hangar's .env), so hangar stopped it.\n", limit)
	return fmt.Errorf("job stopped at the %s-minute limit", limit)
}

// stopWatchdog stops worker n's watchdog if one is still running — the one
// this job armed, or one a job that ended without its completed hook left.
func (f *Fleet) stopWatchdog(n int, table procTable) {
	body, err := os.ReadFile(filepath.Join(f.jobStateDir(n), "watchdog.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || !table.is(pid, hangarName()) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

// hangarName is this binary's process name, which a watchdog runs under.
func hangarName() string {
	bin, err := selfPath()
	if err != nil {
		return "hangar"
	}
	return filepath.Base(bin)
}
