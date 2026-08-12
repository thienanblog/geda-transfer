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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gedad.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsFile(t *testing.T) {
	path := writeConf(t, `
# A comment, and a blank line above.
name = Living Room NAS
dest = /srv/photos
listen = 0.0.0.0:9000
discovery_port = 47899
advertise = 10.0.0.5, 10.8.0.1
mdns = off
naming_template = "{yyyy}/{original_name}.{ext}"
`)

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "Living Room NAS" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.Dest != "/srv/photos" {
		t.Errorf("dest = %q", cfg.Dest)
	}
	if cfg.Listen != "0.0.0.0:9000" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.DiscoveryPort != 47899 {
		t.Errorf("discovery_port = %d", cfg.DiscoveryPort)
	}
	if len(cfg.Advertise) != 2 || cfg.Advertise[0] != "10.0.0.5" || cfg.Advertise[1] != "10.8.0.1" {
		t.Errorf("advertise = %v", cfg.Advertise)
	}
	if cfg.MDNS {
		t.Error("mdns should be off")
	}
	// The quotes are syntax, not content: a template that kept them would
	// produce filenames with quote characters in them.
	if cfg.NamingTemplate != "{yyyy}/{original_name}.{ext}" {
		t.Errorf("naming_template = %q", cfg.NamingTemplate)
	}
}

// A typo in a key must not be silently ignored: a misdirected destination is
// discovered days later, with the files in the wrong place.
func TestUnknownKeyIsAnError(t *testing.T) {
	path := writeConf(t, "destination = /srv/photos\n")

	_, err := Load(path, true)
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error should name the offending key: %v", err)
	}
}

func TestMalformedLineIsAnError(t *testing.T) {
	path := writeConf(t, "name Living Room\n")
	if _, err := Load(path, true); err == nil {
		t.Fatal("expected an error for a line with no =")
	}
}

func TestMissingFileIsFatalOnlyWhenAskedForByName(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.conf")

	if _, err := Load(missing, false); err != nil {
		t.Errorf("a missing default config is the normal state of a fresh install: %v", err)
	}
	if _, err := Load(missing, true); err == nil {
		t.Error("a config named on the command line must exist")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	path := writeConf(t, "name = From File\ndest = /from/file\n")
	t.Setenv(EnvPrefix+"NAME", "From Env")

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "From Env" {
		t.Errorf("name = %q, environment should win over the file", cfg.Name)
	}
	if cfg.Dest != "/from/file" {
		t.Errorf("dest = %q, an unset variable must not clear a file value", cfg.Dest)
	}
}

func TestBadEnvironmentValueIsAnError(t *testing.T) {
	t.Setenv(EnvPrefix+"DISCOVERY_PORT", "no")
	if _, err := Load(filepath.Join(t.TempDir(), "absent.conf"), false); err == nil {
		t.Fatal("expected an error for a non-numeric port")
	}
}

func TestResolveFillsDerivedValues(t *testing.T) {
	cfg := Default()
	cfg.StateDir = t.TempDir()
	cfg.Dest = ""
	cfg.Listen = "127.0.0.1:47891"

	if err := cfg.Resolve(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cfg.StateDir, "Photos"); cfg.Dest != want {
		t.Errorf("dest = %q, want %q", cfg.Dest, want)
	}
	if want := filepath.Join(cfg.StateDir, "gedad.sock"); cfg.ControlSocket != want {
		t.Errorf("control socket = %q, want %q", cfg.ControlSocket, want)
	}
	// Advertising the wrong port hands peers an address that refuses
	// connections, so it tracks the listen port unless set explicitly.
	if cfg.AdvertisePort != 47891 {
		t.Errorf("advertise_port = %d, want the listen port", cfg.AdvertisePort)
	}
}

func TestResolveKeepsExplicitAdvertisePort(t *testing.T) {
	// The published-port case: the container listens on 47891 and the host
	// maps it to 8443, which is the port a phone must be told about.
	cfg := Default()
	cfg.StateDir = t.TempDir()
	cfg.Listen = ":47891"
	cfg.AdvertisePort = 8443

	if err := cfg.Resolve(); err != nil {
		t.Fatal(err)
	}
	if cfg.AdvertisePort != 8443 {
		t.Errorf("advertise_port = %d, want 8443", cfg.AdvertisePort)
	}
}

func TestResolveRejectsUnusableValues(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"empty state dir": func(c *Config) { c.StateDir = "" },
		"listen with no port": func(c *Config) {
			c.Listen = "127.0.0.1"
		},
		"discovery port out of range": func(c *Config) { c.DiscoveryPort = 70000 },
		"unknown log level":           func(c *Config) { c.LogLevel = "loud" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.StateDir = t.TempDir()
			mutate(&cfg)
			if err := cfg.Resolve(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSetAcceptsEveryDocumentedKey(t *testing.T) {
	// Keys() feeds both the environment lookup and the -set help text, so a
	// key that is listed but not settable would be documented and broken.
	values := map[string]string{
		"name":            "NAS",
		"state_dir":       "/var/lib/geda",
		"dest":            "/data",
		"listen":          ":47891",
		"discovery_port":  "47890",
		"advertise_port":  "47891",
		"advertise":       "10.0.0.1",
		"mdns":            "true",
		"discovery":       "false",
		"naming_template": "{original_name}.{ext}",
		"log_level":       "debug",
		"control_socket":  "/run/geda/gedad.sock",
	}

	var cfg Config
	for _, key := range Keys() {
		value, ok := values[key]
		if !ok {
			t.Fatalf("key %q is listed by Keys() but has no test value; add one", key)
		}
		if err := cfg.Set(key, value); err != nil {
			t.Errorf("Set(%q): %v", key, err)
		}
	}
}
