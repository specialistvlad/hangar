package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// The hooks are how the job time limit reaches the runner at all, so the
// worker .env must name both, at the scripts the worker directory holds.
func TestWorkerEnvNamesTheJobHooks(t *testing.T) {
	f := New(&config.Config{Root: "/r"})
	env := f.workerEnv(2)
	for _, want := range []string{
		"ACTIONS_RUNNER_HOOK_JOB_STARTED=/r/workers/w2/" + startedHook + "\n",
		"ACTIONS_RUNNER_HOOK_JOB_COMPLETED=/r/workers/w2/" + completedHook + "\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("worker .env missing %q\ngot:\n%s", want, env)
		}
	}
}

// A hook runs in the job's checkout, which may hold a go.mod of its own, so
// the script pins HANGAR_ROOT; and a root with a quote or a space in it must
// still reach hangar as one word.
func TestHookScriptsPinTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "it's my hangar")
	f := New(&config.Config{Root: root})
	scripts, err := f.hookScripts(4)
	if err != nil {
		t.Fatal(err)
	}
	for stage, name := range map[string]string{"started": startedHook, "completed": completedHook} {
		body := scripts[name]
		if !strings.HasSuffix(body, " job-hook "+stage+" 4\n") {
			t.Errorf("%s does not call `job-hook %s 4`:\n%s", name, stage, body)
		}
		// Run the script's command line through sh with hangar — here the
		// test binary, which must not run — swapped for printenv.
		bin, err := selfPath()
		if err != nil {
			t.Fatal(err)
		}
		line := body[strings.LastIndex(body[:len(body)-1], "\n")+1:]
		if !strings.Contains(line, "exec "+shQuote(bin)+" ") {
			t.Fatalf("%s does not exec this binary:\n%s", name, body)
		}
		line = strings.Replace(line, "exec "+shQuote(bin), "exec printenv HANGAR_ROOT #", 1)
		out, err := exec.CommandContext(t.Context(), "sh", "-c", line).Output()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(out) != root+"\n" {
			t.Errorf("%s: HANGAR_ROOT did not survive quoting; got %q", name, out)
		}
	}
}

func TestMinutes(t *testing.T) {
	for n, want := range map[int]string{1: "1 minute", 2: "2 minutes", 30: "30 minutes"} {
		if got := minutes(n); got != want {
			t.Errorf("minutes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/plain":   "'/plain'",
		"a b":      "'a b'",
		"it's":     `'it'\''s'`,
		"$HOME`x`": "'$HOME`x`'",
	} {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// With the limit off the hook arms nothing and says so in the job log.
func TestJobStartedWithNoLimit(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), JobTimeoutMinutes: 0})
	stale := filepath.Join(f.jobStateDir(1), expiredMarker)
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := f.jobStarted(1, &out, jobTree(), 410); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no job time limit") {
		t.Errorf("job log = %q, want it to say there is no limit", out.String())
	}
	if _, err := os.Stat(f.jobStateDir(1)); err == nil {
		t.Error("an earlier job's state must be cleared, or its marker would fail this job")
	}
}

// Outside a job there is no Runner.Worker to time, and a hook that cannot
// enforce the limit must fail the job rather than let it run unlimited.
func TestJobStartedFailsWithoutARunnerWorker(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), JobTimeoutMinutes: 30})
	if err := f.jobStarted(1, &strings.Builder{}, jobTree(), 900); err == nil {
		t.Fatal("JobStarted outside a Runner.Worker must fail")
	}
}

// A job that ran past the limit fails at its completed hook, with an
// annotation naming the limit it was given — even when the step that was
// stopped had continue-on-error. One that did not passes through untouched.
func TestJobCompleted(t *testing.T) {
	f := New(&config.Config{Root: t.TempDir(), JobTimeoutMinutes: 30})
	var out strings.Builder
	if err := f.JobCompleted(1, &out); err != nil || out.Len() != 0 {
		t.Errorf("a job within its limit: err = %v, log = %q; want nil and nothing", err, out.String())
	}

	if err := os.MkdirAll(f.jobStateDir(1), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.jobStateDir(1), expiredMarker), []byte("45\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := f.JobCompleted(1, &out); err == nil {
		t.Error("a job past its limit must fail at the completed hook")
	}
	if !strings.HasPrefix(out.String(), "::error title=Job time limit::") || !strings.Contains(out.String(), "45-minute limit") {
		t.Errorf("job log = %q, want an error annotation naming the 45-minute limit", out.String())
	}
	if _, err := os.Stat(f.jobStateDir(1)); err == nil {
		t.Error("the completed hook must leave no state for the next job")
	}
}
