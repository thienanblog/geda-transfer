// Copyright 2026 Geda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command gedad is the headless Geda Transfer receiver: a NAS, a Linux box, a
// container. It is a thin layer over core/ (AGENTS.md §2) plus the parts a
// machine with no screen needs -- a config file, a control socket, and a QR
// code drawn in the terminal.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/geda/geda-transfer/cli/internal/config"
	"github.com/geda/geda-transfer/cli/internal/control"
	"github.com/geda/geda-transfer/cli/internal/daemon"
	"github.com/geda/geda-transfer/cli/internal/qrterm"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "gedad:", err)
		os.Exit(1)
	}
}

const usage = `gedad -- headless Geda Transfer receiver

Usage:
  gedad [command] [flags]

Commands:
  run        Serve until stopped (the default)
  pair       Show a pairing QR code for a phone to scan
  devices    List paired devices
  send       Queue files for a device to collect
  queue      Show what is waiting for a device
  unpair     Revoke a device's token
  status     Report what the running daemon is doing
  version    Print the version

Run "gedad <command> -h" for the flags of one command.
`

func run(args []string) error {
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}

	switch command {
	case "run", "serve":
		return cmdRun(args)
	case "pair":
		return cmdPair(args)
	case "devices":
		return cmdDevices(args)
	case "send":
		return cmdSend(args)
	case "queue":
		return cmdQueue(args)
	case "unpair":
		return cmdUnpair(args)
	case "status":
		return cmdStatus(args)
	case "version":
		fmt.Println("gedad", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// flagSet builds the flags every command shares: where the config is, and the
// handful of settings worth overriding without editing a file.
//
// Every other setting is reachable through -set key=value and through the
// GEDA_* environment, so no setting is flag-only or file-only.
func flagSet(name string) (*flag.FlagSet, *configFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	cf := &configFlags{}

	fs.StringVar(&cf.path, "config", "", "config file (default "+config.DefaultPath()+")")
	fs.StringVar(&cf.stateDir, "state-dir", "", "state directory: ledger, TLS identity, control socket")
	fs.StringVar(&cf.socket, "socket", "", "control socket path")
	fs.Var(&cf.overrides, "set", "override one setting as key=value; repeatable\n(keys: "+strings.Join(config.Keys(), ", ")+")")

	return fs, cf
}

type configFlags struct {
	path      string
	stateDir  string
	socket    string
	dest      string
	listen    string
	name      string
	logLevel  string
	overrides listFlag
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// load resolves the configuration: file, then environment, then flags.
func (cf *configFlags) load() (config.Config, error) {
	path := cf.path
	explicit := path != ""
	if !explicit {
		path = config.DefaultPath()
	}

	cfg, err := config.Load(path, explicit)
	if err != nil {
		return config.Config{}, err
	}

	for _, raw := range cf.overrides {
		key, value, found := strings.Cut(raw, "=")
		if !found {
			return config.Config{}, fmt.Errorf("-set %q must be key=value", raw)
		}
		if err := cfg.Set(key, value); err != nil {
			return config.Config{}, err
		}
	}

	for key, value := range map[string]string{
		"state_dir":      cf.stateDir,
		"control_socket": cf.socket,
		"dest":           cf.dest,
		"listen":         cf.listen,
		"name":           cf.name,
		"log_level":      cf.logLevel,
	} {
		if value == "" {
			continue
		}
		if err := cfg.Set(key, value); err != nil {
			return config.Config{}, err
		}
	}

	if err := cfg.Resolve(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func cmdRun(args []string) error {
	fs, cf := flagSet("run")
	fs.StringVar(&cf.dest, "dest", "", "destination directory for received files")
	fs.StringVar(&cf.listen, "listen", "", "TLS listen address, host:port")
	fs.StringVar(&cf.name, "name", "", "name shown to phones on the network")
	fs.StringVar(&cf.logLevel, "log-level", "", "debug, info, warn, or error")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := cf.load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)

	// SIGTERM is how a container and systemd both ask for a stop, so the
	// daemon must treat it as a clean shutdown rather than being killed with
	// a partial upload half-written.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := daemon.New(ctx, cfg, version, log)
	if err != nil {
		return err
	}
	defer d.Close()

	if err := d.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	log.Info("gedad stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	// Stderr, so that -json output on stdout stays machine-readable.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func cmdPair(args []string) error {
	fs, cf := flagSet("pair")
	var (
		ttl    = fs.Duration("ttl", 0, "how long the code stays valid (default 5m)")
		asJSON = fs.Bool("json", false, "print the offer as JSON instead of a QR code")
		noQR   = fs.Bool("no-qr", false, "print the pairing URI without drawing the code")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	offer, err := client.Pair(ctx, *ttl)
	if err != nil {
		return err
	}

	if *asJSON {
		return printJSON(offer)
	}

	// Piped output is not being read by a person, and ANSI colour blocks in a
	// log file are noise.
	if !*noQR && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := drawQR(offer.URI); err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Println("Scan this with Geda Transfer on your phone.")
	fmt.Printf("  fingerprint  %s\n", offer.Fingerprint)
	fmt.Printf("  addresses    %s\n", strings.Join(offer.Addrs, ", "))
	fmt.Printf("  valid until  %s\n", offer.ExpiresAt.Local().Format(time.RFC1123))
	fmt.Printf("  uri          %s\n", offer.URI)
	fmt.Println()
	fmt.Println("The code is single-use and expires; run `gedad pair` again for another.")
	return nil
}

// drawQR prints the code, unless the window is too narrow for it.
//
// A code wider than the terminal wraps, and a wrapped QR code cannot be
// scanned at all -- so printing one anyway would waste the user's time
// twice: once staring at it, once working out why the phone will not read it.
func drawQR(uri string) error {
	columns, err := qrterm.Columns(uri)
	if err != nil {
		return err
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width < columns {
		fmt.Printf("\nThis window is %d columns and the code needs %d.\n"+
			"Widen the terminal and run `gedad pair` again, or type the URI below into the app.\n",
			width, columns)
		return nil
	}

	fmt.Println()
	return qrterm.Write(os.Stdout, uri)
}

func cmdDevices(args []string) error {
	fs, cf := flagSet("devices")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	devices, err := client.Devices(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(devices)
	}
	if len(devices) == 0 {
		fmt.Println("No devices paired yet. Run `gedad pair` and scan the code.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE ID\tNAME\tPLATFORM\tFILES\tSIZE\tQUEUED\tLAST SEEN\tSTATUS")
	for _, d := range devices {
		last := "never"
		if d.LastSeenAt != nil {
			last = d.LastSeenAt.Local().Format(time.RFC3339)
		}
		state := "paired"
		if d.Revoked {
			state = "revoked"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
			d.ID, d.Name, d.Platform, d.Files, humanBytes(d.Bytes), d.Queued, last, state)
	}
	return w.Flush()
}

// cmdSend queues files for a phone.
//
// Nothing is transferred here, and the wording says so: a suspended iPhone
// cannot be pushed to (AGENTS.md §3.7), so this puts files on offer and the
// phone collects them the next time somebody opens the app. Reporting "sent"
// would be a lie that shows up hours later as a missing file.
func cmdSend(args []string) error {
	fs, cf := flagSet("send")
	device := fs.String("device", "", "device id to send to (required)")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *device == "" {
		return errors.New("usage: gedad send -device <device-id> <file>...")
	}
	if fs.NArg() == 0 {
		return errors.New("no files given")
	}

	// Resolved here so that a relative path means what the user typed it to
	// mean, in the shell they typed it in, rather than whatever directory the
	// daemon happens to have been started from.
	paths := make([]string, 0, fs.NArg())
	for _, arg := range fs.Args() {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return fmt.Errorf("%s: %w", arg, err)
		}
		paths = append(paths, abs)
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	queued, err := client.Send(ctx, *device, paths)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(queued)
	}

	var total int64
	for _, q := range queued {
		total += q.Size
	}
	fmt.Printf("%s queued for %s (%s).\n", plural(len(queued), "file"), *device, humanBytes(total))
	fmt.Println("They will transfer when that device next opens Geda Transfer.")
	return nil
}

func cmdQueue(args []string) error {
	fs, cf := flagSet("queue")
	device := fs.String("device", "", "device id (required)")
	cancelID := fs.String("cancel", "", "withdraw one queued file by id")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *device == "" {
		return errors.New("usage: gedad queue -device <device-id>")
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if *cancelID != "" {
		if err := client.CancelSend(ctx, *device, *cancelID); err != nil {
			return err
		}
		fmt.Printf("%s withdrawn.\n", *cancelID)
		return nil
	}

	queued, err := client.Outbox(ctx, *device)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(queued)
	}
	if len(queued) == 0 {
		fmt.Printf("Nothing queued for %s.\n", *device)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tFILE\tKIND\tSIZE\tSTATE\tQUEUED")
	for _, q := range queued {
		state := q.State
		if q.Error != "" {
			state += ": " + q.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			q.ID, q.Filename, q.Kind, humanBytes(q.Size), state,
			q.QueuedAt.Local().Format(time.RFC3339))
	}
	return w.Flush()
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func cmdUnpair(args []string) error {
	fs, cf := flagSet("unpair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: gedad unpair <device-id>")
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.Unpair(ctx, fs.Arg(0)); err != nil {
		return err
	}
	// Files are the user's and stay where they are; only the credential goes.
	fmt.Printf("%s can no longer connect. Its files were left in place.\n", fs.Arg(0))
	return nil
}

func cmdStatus(args []string) error {
	fs, cf := flagSet("status")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := connect(cf)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(status)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "version\t%s\n", status.Version)
	fmt.Fprintf(w, "name\t%s\n", status.Name)
	fmt.Fprintf(w, "device id\t%s\n", status.DeviceID)
	fmt.Fprintf(w, "fingerprint\t%s\n", status.Fingerprint)
	fmt.Fprintf(w, "listening on\t%s\n", status.Listen)
	fmt.Fprintf(w, "addresses\t%s\n", strings.Join(status.Addrs, ", "))
	fmt.Fprintf(w, "destination\t%s\n", status.Dest)
	fmt.Fprintf(w, "state\t%s\n", status.StateDir)
	fmt.Fprintf(w, "uptime\t%s\n", time.Since(status.StartedAt).Round(time.Second))
	fmt.Fprintf(w, "paired devices\t%d\n", status.PairedDevices)
	fmt.Fprintf(w, "files received\t%d (%s)\n", status.Files, humanBytes(status.Bytes))
	return w.Flush()
}

func connect(cf *configFlags) (*control.Client, error) {
	cfg, err := cf.load()
	if err != nil {
		return nil, err
	}
	return control.Dial(cfg.ControlSocket), nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
