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

// Package config reads gedad's configuration.
//
// The format is deliberately boring: `key = value`, one per line, `#` for
// comments. A NAS user edits this over SSH in whatever editor the box happens
// to have, and a Docker user sets the same keys as GEDA_* environment
// variables without learning a second syntax. Unknown keys are an error rather
// than a shrug, because a typo that silently leaves a destination at its
// default is discovered days later, by which time the files are in the wrong
// place.
//
// Precedence, lowest to highest: defaults, file, environment, flags.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/geda/geda-transfer/core/discovery"
)

// EnvPrefix prefixes the environment variable for every key.
const EnvPrefix = "GEDA_"

// Config is a resolved daemon configuration.
type Config struct {
	// Name is what users see when choosing a destination.
	Name string

	// StateDir holds the ledger, the TLS identity, and the control socket.
	// It must survive updates: losing the identity key makes every paired
	// device fail with a pin mismatch (AGENTS.md §3.5).
	StateDir string

	// Dest is the destination directory for received files.
	Dest string

	// Listen is the TLS listen address for transfers.
	Listen string

	// DiscoveryPort is the UDP port probes arrive on.
	DiscoveryPort int

	// AdvertisePort is the TCP port peers are told to connect to. It differs
	// from the listen port only behind a port mapping -- a container
	// published as -p 47891:47891 is the common case, and a container
	// published on another port is why this is configurable at all.
	AdvertisePort int

	// Advertise overrides the candidate address set. Empty means "every local
	// interface address, tunnels included", which is what a host-network
	// deployment wants. A bridged container sees only its private bridge
	// address, which no peer can dial, so that deployment sets this to the
	// host's addresses.
	Advertise []string

	// MDNS runs the L1 responder. Pointless in a bridged container, where
	// multicast does not leave the bridge.
	MDNS bool

	// Discovery runs the UDP responder (L2-L5). Turning it off leaves manual
	// pairing and the stored candidate set, which is all a receiver reached
	// only over WireGuard ever uses.
	Discovery bool

	// NamingTemplate, when set, is written to the ledger at startup so the
	// file is the source of truth on a headless box.
	NamingTemplate string

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// ControlSocket is the Unix socket `gedad pair` and friends talk to.
	// Defaults to StateDir/gedad.sock.
	ControlSocket string
}

// Default returns the configuration used when nothing is set anywhere.
func Default() Config {
	state := defaultStateDir()
	return Config{
		Name:          defaultName(),
		StateDir:      state,
		Dest:          "",
		Listen:        ":" + strconv.Itoa(discovery.DefaultTransferPort),
		DiscoveryPort: discovery.DefaultPort,
		MDNS:          true,
		Discovery:     true,
		LogLevel:      "info",
	}
}

// DefaultPath is where gedad looks for a config file when told nothing.
func DefaultPath() string {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		return "/etc/geda/gedad.conf"
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "gedad.conf"
	}
	return filepath.Join(dir, "geda", "gedad.conf")
}

func defaultName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "Geda Receiver"
	}
	return host
}

func defaultStateDir() string {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		return "/var/lib/geda"
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".geda"
	}
	return filepath.Join(dir, "geda")
}

// keys are every recognised setting. Listing them explicitly is what lets a
// misspelled key fail loudly instead of being ignored.
var keys = map[string]func(*Config, string) error{
	"name":            func(c *Config, v string) error { c.Name = v; return nil },
	"state_dir":       func(c *Config, v string) error { c.StateDir = v; return nil },
	"dest":            func(c *Config, v string) error { c.Dest = v; return nil },
	"listen":          func(c *Config, v string) error { c.Listen = v; return nil },
	"discovery_port":  func(c *Config, v string) error { return setInt(&c.DiscoveryPort, v) },
	"advertise_port":  func(c *Config, v string) error { return setInt(&c.AdvertisePort, v) },
	"advertise":       func(c *Config, v string) error { c.Advertise = splitList(v); return nil },
	"mdns":            func(c *Config, v string) error { return setBool(&c.MDNS, v) },
	"discovery":       func(c *Config, v string) error { return setBool(&c.Discovery, v) },
	"naming_template": func(c *Config, v string) error { c.NamingTemplate = v; return nil },
	"log_level":       func(c *Config, v string) error { c.LogLevel = v; return nil },
	"control_socket":  func(c *Config, v string) error { c.ControlSocket = v; return nil },
}

// Keys lists the recognised settings, sorted, for help output and docs.
func Keys() []string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Load reads path over the defaults, then applies the environment.
//
// A missing file is only an error when the caller asked for that file by name.
// The default path not existing is the normal state of a fresh install: the
// daemon works with zero configuration (AGENTS.md §4).
func Load(path string, explicit bool) (Config, error) {
	cfg := Default()

	f, err := os.Open(path)
	switch {
	case err == nil:
		defer f.Close()
		if err := parse(f, &cfg); err != nil {
			return Config{}, fmt.Errorf("read %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
	default:
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	if err := applyEnv(&cfg, os.Getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Set applies one key/value pair, as the file and the environment both do.
func (c *Config) Set(key, value string) error {
	apply, ok := keys[strings.ToLower(strings.TrimSpace(key))]
	if !ok {
		return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(Keys(), ", "))
	}
	return apply(c, strings.TrimSpace(value))
}

func parse(r io.Reader, cfg *Config) error {
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		key, value, found := strings.Cut(text, "=")
		if !found {
			return fmt.Errorf("line %d: expected `key = value`, got %q", line, text)
		}
		// Values are taken literally apart from surrounding space and one
		// optional layer of quotes, which is what a path containing a `#`
		// needs. There is no escaping beyond that, on purpose: this file
		// holds paths and ports, not a language.
		if err := cfg.Set(key, unquote(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	return scanner.Err()
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func applyEnv(cfg *Config, lookup func(string) string) error {
	for _, key := range Keys() {
		name := EnvPrefix + strings.ToUpper(key)
		value := lookup(name)
		if value == "" {
			continue
		}
		if err := cfg.Set(key, value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// Resolve fills in what depends on other settings and rejects what cannot
// work. It is called once, after flags have been applied.
func (c *Config) Resolve() error {
	c.StateDir = strings.TrimSpace(c.StateDir)
	if c.StateDir == "" {
		return errors.New("state_dir must not be empty")
	}
	abs, err := filepath.Abs(c.StateDir)
	if err != nil {
		return fmt.Errorf("resolve state_dir: %w", err)
	}
	c.StateDir = abs

	if strings.TrimSpace(c.Dest) == "" {
		c.Dest = filepath.Join(c.StateDir, "Photos")
	}
	if c.Dest, err = filepath.Abs(c.Dest); err != nil {
		return fmt.Errorf("resolve dest: %w", err)
	}

	if strings.TrimSpace(c.Name) == "" {
		c.Name = defaultName()
	}

	if c.ControlSocket == "" {
		c.ControlSocket = filepath.Join(c.StateDir, "gedad.sock")
	}

	if c.Listen == "" {
		c.Listen = ":" + strconv.Itoa(discovery.DefaultTransferPort)
	}
	port, err := listenPort(c.Listen)
	if err != nil {
		return err
	}
	if c.AdvertisePort == 0 {
		// Port 0 means "any free port", which cannot be advertised until the
		// listener exists; the daemon fills it in from the bound address.
		c.AdvertisePort = port
	}

	if c.DiscoveryPort <= 0 || c.DiscoveryPort > 65535 {
		return fmt.Errorf("discovery_port %d is not a valid port", c.DiscoveryPort)
	}
	if c.AdvertisePort < 0 || c.AdvertisePort > 65535 {
		return fmt.Errorf("advertise_port %d is not a valid port", c.AdvertisePort)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level %q is not one of debug, info, warn, error", c.LogLevel)
	}
	return nil
}

func listenPort(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("listen %q must be host:port: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("listen %q has no valid port", addr)
	}
	return port, nil
}

func setInt(dst *int, v string) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%q is not a number", v)
	}
	*dst = n
	return nil
}

func setBool(dst *bool, v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		*dst = true
	case "0", "false", "no", "off":
		*dst = false
	default:
		return fmt.Errorf("%q is not a boolean", v)
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
