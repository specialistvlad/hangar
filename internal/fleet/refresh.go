package fleet

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// workerFile is one file hangar writes into a worker from its settings.
type workerFile struct {
	path string
	body string
	mode os.FileMode
}

// workerFiles is everything in worker n that follows from .env and the hangar
// binary: the runner's .env and .path, and the job hooks. Provisioning writes
// them; refresh rewrites them when they no longer match.
func (f *Fleet) workerFiles(n int) ([]workerFile, error) {
	dir := f.cfg.WorkerDir(n)
	hooks, err := f.hookScripts(n)
	if err != nil {
		return nil, err
	}
	return []workerFile{
		{filepath.Join(dir, ".env"), f.workerEnv(n), 0o600},
		{filepath.Join(dir, ".path"), f.runnerPath(n) + "\n", 0o644},
		{filepath.Join(dir, startedHook), hooks[startedHook], 0o700},
		{filepath.Join(dir, completedHook), hooks[completedHook], 0o700},
	}, nil
}

// writeWorkerFiles writes worker n's workerFiles.
func (f *Fleet) writeWorkerFiles(n int) error {
	files, err := f.workerFiles(n)
	if err != nil {
		return err
	}
	for _, wf := range files {
		if err := os.WriteFile(wf.path, []byte(wf.body), wf.mode); err != nil {
			return err
		}
	}
	return nil
}

// stale reports whether any of worker n's workerFiles differs from what the
// current settings and binary would write.
func (f *Fleet) stale(n int) (bool, error) {
	files, err := f.workerFiles(n)
	if err != nil {
		return false, err
	}
	for _, wf := range files {
		have, err := os.ReadFile(wf.path)
		if err != nil || !bytes.Equal(have, []byte(wf.body)) {
			return true, nil
		}
	}
	return false, nil
}

// refresh brings the workers a scale to n keeps up to date with the current
// settings. Scale is otherwise a diff that never touches a worker it keeps, so
// without this a worker provisioned by an older hangar — before the job hooks,
// say — or before a change to a setting a worker's files carry would keep its
// old files for good.
//
// The runner reads .env only when its service starts, so a running worker is
// restarted after its files are rewritten — but never while it runs a job:
// a restart then would fail the job. A busy worker is left as it is, files
// included, and the next scale picks it up; a stopped one only gets its files,
// which its next start reads. There remains the moment between seeing a worker
// idle and restarting it, in which GitHub may hand it a job; the runner fails
// that job at once, as it would on any restart.
func (f *Fleet) refresh(n int, ws []Worker, supervisorErr error, progress func(string)) error {
	var table procTable
	for _, w := range ws {
		if !w.Ready || w.Index > n {
			continue
		}
		isStale, err := f.stale(w.Index)
		if err != nil {
			return fmt.Errorf("checking w%d: %w", w.Index, err)
		}
		if !isStale {
			continue
		}
		if supervisorErr != nil {
			return fmt.Errorf("cannot tell which workers are running, so not refreshing w%d: %w — retry", w.Index, supervisorErr)
		}
		if w.Running {
			if table == nil {
				if table, err = readProcTable(); err != nil {
					return fmt.Errorf("cannot tell which workers are running a job, so not refreshing: %w", err)
				}
			}
			if table.runsJob(w.PID) {
				progress(fmt.Sprintf("%s is running a job — scale again once it is idle to apply the new settings", w.Name))
				continue
			}
		}
		if err := f.writeWorkerFiles(w.Index); err != nil {
			return fmt.Errorf("refreshing w%d: %w", w.Index, err)
		}
		if w.Running {
			if err := f.startService(w.Index); err != nil {
				return fmt.Errorf("restarting w%d: %w", w.Index, err)
			}
		}
		progress(fmt.Sprintf("refreshed %s", w.Name))
	}
	return nil
}
