package board

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

const minimalConfig = "[server]\nurl = \"https://ptouch.example.com\"\n"

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := defaultConfig()
	want.Server.URL = "https://ptouch.example.com"
	if c != want {
		t.Errorf("got %+v, want defaults %+v", c, want)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := LoadConfig("../../deploy/board/config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if want := defaultConfig(); c.BusyTimeout != want.BusyTimeout || c.Server.PollInterval != want.Server.PollInterval {
		t.Errorf("example doesn't match the defaults: %+v", c)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `
busy_timeout = "5s"
print_timeout = "1m"
max_label_dots = 1000

[server]
url = "http://localhost:8080"
token_file = "/tmp/token"
poll_interval = "5s"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := ServerConfig{URL: "http://localhost:8080", TokenFile: "/tmp/token", PollInterval: Duration{5 * time.Second}}
	if c.Server != want || c.BusyTimeout.Duration != 5*time.Second ||
		c.PrintTimeout.Duration != time.Minute || c.MaxLabelDots != 1000 {
		t.Errorf("got %+v", c)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name, config, want string
	}{
		{"unknown setting", minimalConfig + "lisen = \":80\"", `unknown setting`},
		{"old bluetooth section", minimalConfig + "[bluetooth]\nname = \"x\"", `unknown setting "bluetooth`},
		{"no url", "", "server.url: required"},
		{"plain http", "[server]\nurl = \"http://ptouch.example.com\"", "must be an https:// URL"},
		{"no host", "[server]\nurl = \"https://\"", "must be an https:// URL"},
		{"zero poll", minimalConfig + "poll_interval = \"0s\"", "server.poll_interval must be positive"},
		{"empty token file", minimalConfig + "token_file = \"\"", "server.token_file is required"},
		{"bad duration", "busy_timeout = \"soon\"\n" + minimalConfig, "busy_timeout"},
		{"zero timeout", "print_timeout = \"0s\"\n" + minimalConfig, "print_timeout must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tt.config))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
