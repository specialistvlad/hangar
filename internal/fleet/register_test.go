package fleet

import (
	"strings"
	"testing"

	"github.com/specialistvlad/hangar/internal/config"
)

// A runner token must never appear in argv: `ps` shows a process's command line
// to every user on the machine, while its environment is readable only by the
// owner. This pins that split, since putting `--token` back would look like a
// harmless simplification and would leak on every scale operation.
func TestRegisterArgsCarryNoToken(t *testing.T) {
	const token = "AAAAsecretregistrationtoken"
	f := New(&config.Config{Root: "/r", Org: "acme", NamePrefix: "mac-w", Group: "g", Labels: "l"})

	args := strings.Join(f.registerArgs(2), " ")
	if strings.Contains(args, token) || strings.Contains(args, "--token") {
		t.Errorf("registration argv must not carry the token, got: %s", args)
	}
	// The rest of the invocation still has to be there.
	for _, want := range []string{"--unattended", "--url https://github.com/acme", "--name mac-w2"} {
		if !strings.Contains(args, want) {
			t.Errorf("registration argv missing %q, got: %s", want, args)
		}
	}

	// The runner reads any config argument as ACTIONS_RUNNER_INPUT_<ARG>; the
	// exact spelling is what makes the out-of-band handoff work at all, and a
	// typo would fall back to an interactive prompt.
	env := tokenEnv(token)
	if len(env) != 1 || env[0] != "ACTIONS_RUNNER_INPUT_TOKEN="+token {
		t.Errorf("tokenEnv must hand the token to the runner out of band, got: %v", env)
	}
}
