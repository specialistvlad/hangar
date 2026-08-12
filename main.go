// Command hangar manages a fleet of GitHub Actions self-hosted runners on a
// single Mac, giving each one isolated credential state while they share the
// docker daemon and its build cache.
//
// It is normally driven through the Makefile rather than invoked directly.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/config"
	"github.com/specialistvlad/hangar/internal/credhelper"
	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/tui"
)

const usage = `hangar — GitHub Actions runner fleet for one Mac

  hangar scale <0-32>   reconcile the fleet to N workers
  hangar watch          live dashboard (quitting leaves runners running)
  hangar update         fetch the newest runner release
  hangar status         one-shot fleet summary

Normally driven via the Makefile: make 4 · make watch · make 0`

func main() {
	// Docker executes docker-credential-<credsStore> from PATH. hangar symlinks
	// itself under that name into each worker, so the same binary answers both.
	if filepath.Base(os.Args[0]) == "docker-credential-"+credhelper.Name {
		if err := credhelper.Run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
			// A miss has already written the message Docker matches to stdout.
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hangar: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}

	root, err := config.FindRoot()
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	f := fleet.New(cfg)

	switch args[0] {
	case "scale":
		if len(args) < 2 {
			return fmt.Errorf("scale needs a count, e.g. `hangar scale 4`")
		}
		n, err := config.ParseCount(args[1])
		if err != nil {
			return err
		}
		if err := cfg.RequireGitHub(); err != nil {
			return err
		}
		if err := f.Scale(n, logf); err != nil {
			return err
		}
		return status(f)

	case "update":
		rel, err := f.LatestRelease()
		if err != nil {
			return err
		}
		if _, err := f.EnsureTarball(rel, logf); err != nil {
			return err
		}
		logf(fmt.Sprintf("runner %s ready", rel.Version))
		return nil

	case "watch":
		if len(f.List()) == 0 {
			return fmt.Errorf("no workers — run `make 4` first")
		}
		// AltScreen keeps the dashboard from shredding the user's scrollback.
		p := tea.NewProgram(tui.New(f), tea.WithAltScreen(), tea.WithMouseCellMotion())
		_, err := p.Run()
		return err

	case "status":
		return status(f)

	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
}

func status(f *fleet.Fleet) error {
	ws := f.List()
	cfg := f.Config()
	fmt.Printf("%d worker(s) · %s/%s\n", len(ws), cfg.Org, orDefault(cfg.Group))
	fmt.Printf("  token: %s\n", tokenState(f, cfg))
	for _, w := range ws {
		state := "stopped"
		if w.Running {
			state = fmt.Sprintf("running (pid %d)", w.PID)
		}
		fmt.Printf("  %-20s %s\n", w.Name, state)
	}
	return nil
}

// tokenState turns the credential check into one readable line, so `hangar
// status` doubles as the preflight before scaling anything.
func tokenState(f *fleet.Fleet, cfg *config.Config) string {
	if err := cfg.RequireGitHub(); err != nil {
		return "missing — " + err.Error()
	}
	if err := f.CheckAuth(); err != nil {
		return "REJECTED — " + err.Error()
	}
	return "ok, can manage " + cfg.Org + " runners"
}

func logf(msg string) { fmt.Println("  " + msg) }

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}
