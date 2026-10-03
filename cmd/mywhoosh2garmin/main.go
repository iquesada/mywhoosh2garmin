// Command mywhoosh2garmin uploads new MyWhoosh workouts to Garmin Connect.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"mywhoosh2garmin/internal/garmin"
	"mywhoosh2garmin/internal/mywhoosh"
	"mywhoosh2garmin/internal/state"
	"mywhoosh2garmin/internal/syncer"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	envConfigDir        = "MYWHOOSH2GARMIN_CONFIG_DIR"
	envMyWhooshEmail    = "MYWHOOSH_EMAIL"
	envMyWhooshPassword = "MYWHOOSH_PASSWORD"

	defaultLimit = 10
)

const usage = `mywhoosh2garmin uploads new MyWhoosh workouts to Garmin Connect.

Usage:
  mywhoosh2garmin <command> [flags]

Commands:
  login     Sign in to Garmin Connect in the browser and store the session.
  list      List the most recent MyWhoosh activities and their sync status.
  sync      Upload MyWhoosh activities that are not in Garmin Connect yet.
  version   Print the version.

Environment:
  MYWHOOSH_EMAIL, MYWHOOSH_PASSWORD   MyWhoosh credentials (list, sync).
  MYWHOOSH2GARMIN_CONFIG_DIR          Where the Garmin session and the sync
                                      state are stored. Defaults to the user
                                      config directory.

Run "mywhoosh2garmin <command> -h" for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "login":
		return runLogin(ctx, args, stdout, stderr)
	case "list":
		return runList(ctx, args, stdout, stderr)
	case "sync":
		return runSync(ctx, args, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// paths are the files the tool keeps between runs.
type paths struct {
	garminTokens string
	state        string
}

func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configDir := fs.String("config-dir", os.Getenv(envConfigDir),
		"directory for the Garmin session and the sync state (default: user config directory)")
	return fs, configDir
}

func resolvePaths(configDir string) (paths, error) {
	if configDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return paths{}, fmt.Errorf("find config directory: %w (set %s)", err, envConfigDir)
		}
		configDir = filepath.Join(base, "mywhoosh2garmin")
	}
	return paths{
		garminTokens: filepath.Join(configDir, "garmin-tokens.json"),
		state:        filepath.Join(configDir, "state.json"),
	}, nil
}

func runLogin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, configDir := newFlagSet("login", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolvePaths(*configDir)
	if err != nil {
		return err
	}
	tokens, err := garmin.NewAuth(stdout).BrowserLogin(ctx)
	if err != nil {
		return err
	}
	if err := (garmin.FileStore{Path: p.garminTokens}).Save(tokens); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Signed in to Garmin Connect. Session stored in %s\n", p.garminTokens)
	return nil
}

func runList(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, configDir := newFlagSet("list", stderr)
	limit := fs.Int("limit", defaultLimit, "number of recent activities to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolvePaths(*configDir)
	if err != nil {
		return err
	}
	source, err := newMyWhoosh()
	if err != nil {
		return err
	}
	st, err := state.Load(p.state)
	if err != nil {
		return err
	}
	activities, err := source.List(ctx, *limit)
	if err != nil {
		return err
	}
	if len(activities) == 0 {
		fmt.Fprintln(stdout, "No activities found.")
		return nil
	}
	for _, a := range activities {
		status := "pending"
		if st.IsSynced(a.ID) {
			status = "synced"
		}
		when := "unknown date    "
		if !a.StartTime.IsZero() {
			when = a.StartTime.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(stdout, "%-8s %s  %-12s %s\n", status, when, a.ID, a.Name)
	}
	return nil
}

func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, configDir := newFlagSet("sync", stderr)
	limit := fs.Int("limit", defaultLimit, "number of recent MyWhoosh activities to check")
	dryRun := fs.Bool("dry-run", false, "show what would be uploaded without uploading")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolvePaths(*configDir)
	if err != nil {
		return err
	}
	source, err := newMyWhoosh()
	if err != nil {
		return err
	}
	st, err := state.Load(p.state)
	if err != nil {
		return err
	}
	destination := garmin.NewClient(garmin.NewAuth(stdout), garmin.FileStore{Path: p.garminTokens})

	s := &syncer.Syncer{
		Source:      source,
		Destination: destination,
		State:       st,
		Log:         stdout,
		DryRun:      *dryRun,
	}
	res, err := s.Run(ctx, *limit)
	fmt.Fprintf(stdout, "Uploaded: %d, already synced: %d, already in Garmin: %d, failed: %d\n",
		res.Uploaded, res.AlreadySynced, res.AlreadyInDestination, res.Failed)
	return err
}

func newMyWhoosh() (*mywhoosh.Client, error) {
	email := os.Getenv(envMyWhooshEmail)
	password := os.Getenv(envMyWhooshPassword)
	if email == "" || password == "" {
		return nil, fmt.Errorf("set %s and %s with your MyWhoosh credentials", envMyWhooshEmail, envMyWhooshPassword)
	}
	return mywhoosh.New(email, password), nil
}
