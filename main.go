// Command hangar manages a fleet of GitHub Actions self-hosted runners on a
// single machine — a Mac or a Linux host — giving each one isolated credential
// state while they share the docker daemon and its build cache.
//
// It is normally driven through the Makefile rather than invoked directly.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/specialistvlad/hangar/internal/config"
	"github.com/specialistvlad/hangar/internal/credhelper"
	"github.com/specialistvlad/hangar/internal/exporter"
	"github.com/specialistvlad/hangar/internal/fleet"
	"github.com/specialistvlad/hangar/internal/tui"
)

const usage = `hangar — GitHub Actions runner fleet for one machine

  hangar scale <0-32>   reconcile the fleet to N workers
  hangar kill           stop and delete every worker locally, without GitHub
  hangar watch          live dashboard; +/- scales (quitting leaves runners running)
  hangar update         fetch the newest runner release
  hangar status         one-shot fleet summary
  hangar serve          Prometheus metrics on METRICS_ADDR (foreground)
  hangar metrics start  run serve as a service, restarted on the current binary
  hangar metrics stop   stop and remove that service

Normally driven via the Makefile: make 4 · make watch · make 0 · make kill · make metrics`

// Set by the Makefile from git; reported by hangar_build_info.
var (
	version = "dev"
	commit  = "unknown"
)

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

	case "kill":
		// No RequireGitHub and no CheckAuth: a kill is what is left when the
		// token is the thing that is broken, so it must never consult one.
		n, left := f.Kill(logf)
		if n == 0 {
			logf("no workers to kill")
			return nil
		}
		if left > 0 {
			logf(fmt.Sprintf("killed %d worker(s), %d left on disk — see warnings above; still "+
				"registered on GitHub as offline, remove them there once GH_TOKEN works", n, left))
			return fmt.Errorf("%d worker(s) left on disk", left)
		}
		logf(fmt.Sprintf("killed %d worker(s) — still registered on GitHub as offline, "+
			"remove them there once GH_TOKEN works", n))
		return nil

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
		// AltScreen keeps the dashboard from shredding the user's scrollback.
		p := tea.NewProgram(tui.New(f), tea.WithAltScreen(), tea.WithMouseCellMotion())
		_, err := p.Run()
		return err

	case "status":
		return status(f)

	case "serve":
		// Stops cleanly on the SIGTERM a service manager sends.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return exporter.Serve(ctx, f, cfg.MetricsAddr, version, commit)

	case "metrics":
		return metrics(f, args[1:])

	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
}

// metrics starts or stops the exporter's service.
func metrics(f *fleet.Fleet, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("metrics needs start or stop, e.g. `hangar metrics start`")
	}
	switch args[0] {
	case "start":
		if err := f.StartMetrics(); err != nil {
			return err
		}
		addr := f.Config().MetricsAddr
		if err := exporter.WaitServing(context.Background(), addr, version, commit, 15*time.Second); err != nil {
			return fmt.Errorf("the exporter service started but is not serving %s: %v — its log, %s, says why; "+
				"it keeps retrying until `make metrics-stop`", addr, err, filepath.Join(f.Config().LogsDir(), "metrics.log"))
		}
		logf("metrics exporter running: " + config.MetricsURL(addr))
		return nil
	case "stop":
		if err := f.StopMetrics(); err != nil {
			return err
		}
		logf("metrics exporter stopped")
		return nil
	}
	return fmt.Errorf("metrics needs start or stop, got %q", args[0])
}

func status(f *fleet.Fleet) error {
	ws := f.List()
	cfg := f.Config()
	fmt.Printf("%d worker(s) · %s/%s\n", len(ws), cfg.Org, orDefault(cfg.Group))
	fmt.Printf("  token: %s\n", tokenState(f, cfg))
	metricsState := "not running (make metrics)"
	if f.MetricsRunning() {
		metricsState = config.MetricsURL(cfg.MetricsAddr)
	}
	fmt.Printf("  metrics: %s\n", metricsState)
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
