package board

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the board's TOML config file.
type Config struct {
	BusyTimeout   Duration     `toml:"busy_timeout"`
	StatusTimeout Duration     `toml:"status_timeout"`
	PrintTimeout  Duration     `toml:"print_timeout"`
	MaxLabelDots  int          `toml:"max_label_dots"`
	Server        ServerConfig `toml:"server"`
}

type ServerConfig struct {
	URL          string   `toml:"url"`           // the command server, e.g. https://ptouch.example.com
	TokenFile    string   `toml:"token_file"`    // the board's bearer token
	PollInterval Duration `toml:"poll_interval"` // how often to ask for a job, fixed
}

// Duration is a time.Duration written in the config as a string like "30s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func defaultConfig() Config {
	return Config{
		BusyTimeout:   Duration{30 * time.Second},
		StatusTimeout: Duration{3 * time.Second},
		PrintTimeout:  Duration{20 * time.Second},
		MaxLabelDots:  3600, // about 50 cm
		Server: ServerConfig{
			TokenFile:    "/etc/ptouch-print-board/token",
			PollInterval: Duration{15 * time.Second},
		},
	}
}

// LoadConfig reads the board's TOML config file at path.
func LoadConfig(path string) (Config, error) {
	c := defaultConfig()
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return Config{}, fmt.Errorf("%s: unknown setting %q", path, u[0].String())
	}

	var errs []error
	for name, d := range map[string]Duration{
		"busy_timeout": c.BusyTimeout, "status_timeout": c.StatusTimeout, "print_timeout": c.PrintTimeout,
		"server.poll_interval": c.Server.PollInterval,
	} {
		if d.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.MaxLabelDots <= 0 {
		errs = append(errs, errors.New("max_label_dots must be positive"))
	}
	if err := checkURL(c.Server.URL); err != nil {
		errs = append(errs, fmt.Errorf("server.url: %w", err))
	}
	if c.Server.TokenFile == "" {
		errs = append(errs, errors.New("server.token_file is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// checkURL insists on HTTPS, since the token travels in a header. Plain HTTP
// is allowed only to this machine, for testing.
func checkURL(s string) error {
	if s == "" {
		return errors.New("required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	if u.Host == "" || u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("%q must be an https:// URL", s)
	}
	return nil
}
