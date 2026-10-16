package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, config string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	want := defaults()
	if c.Bluetooth != want.Bluetooth || c.BusyTimeout != want.BusyTimeout || c.StatusTimeout != want.StatusTimeout ||
		c.PrintTimeout != want.PrintTimeout || c.MaxLabelDots != want.MaxLabelDots {
		t.Errorf("got %+v, want defaults %+v", c, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(writeConfig(t, `
busy_timeout = "5s"
print_timeout = "1m"
max_label_dots = 1000

[bluetooth]
name = "Label printer"
adapter = "hci1"
idle_timeout = "5m"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Bluetooth{Name: "Label printer", Adapter: "hci1", IdleTimeout: Duration{5 * time.Minute}}
	if c.Bluetooth != want || c.BusyTimeout.Duration != 5*time.Second ||
		c.PrintTimeout.Duration != time.Minute || c.MaxLabelDots != 1000 {
		t.Errorf("got %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, config, want string
	}{
		{"unknown setting", "lisen = \":80\"", `unknown setting "lisen"`},
		{"old listen setting", "listen = \":8080\"", `unknown setting "listen"`},
		{"old api_key_file setting", "api_key_file = \"api-key\"", `unknown setting "api_key_file"`},
		{"empty name", "[bluetooth]\nname = \"\"", "bluetooth.name"},
		{"long name", "[bluetooth]\nname = \"" + strings.Repeat("x", 30) + "\"", "bluetooth.name"},
		{"short idle timeout", "[bluetooth]\nidle_timeout = \"30s\"", "idle_timeout must be longer"},
		{"bad duration", "busy_timeout = \"soon\"", "busy_timeout"},
		{"zero timeout", "print_timeout = \"0s\"", "print_timeout must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.config))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
