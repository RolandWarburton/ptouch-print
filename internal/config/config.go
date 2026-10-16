// Package config loads the server's TOML config file.
package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

const maxNameLen = 29 // what fits in a scan response

type Config struct {
	BusyTimeout   Duration  `toml:"busy_timeout"`
	StatusTimeout Duration  `toml:"status_timeout"`
	PrintTimeout  Duration  `toml:"print_timeout"`
	MaxLabelDots  int       `toml:"max_label_dots"`
	Bluetooth     Bluetooth `toml:"bluetooth"`
}

type Bluetooth struct {
	Name        string   `toml:"name"`         // advertised device name
	Adapter     string   `toml:"adapter"`      // BlueZ adapter, e.g. "hci0"
	IdleTimeout Duration `toml:"idle_timeout"` // disconnect a client that sends nothing for this long
}

// Duration is a time.Duration written in the config as a string like "30s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func defaults() Config {
	return Config{
		BusyTimeout:   Duration{30 * time.Second},
		StatusTimeout: Duration{3 * time.Second},
		PrintTimeout:  Duration{20 * time.Second},
		MaxLabelDots:  3600, // about 50 cm
		Bluetooth: Bluetooth{
			Name:        "PT-P700",
			Adapter:     "hci0",
			IdleTimeout: Duration{2 * time.Minute},
		},
	}
}

// Load reads the config file at path.
func Load(path string) (Config, error) {
	c := defaults()
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
		"bluetooth.idle_timeout": c.Bluetooth.IdleTimeout,
	} {
		if d.Duration <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.MaxLabelDots <= 0 {
		errs = append(errs, errors.New("max_label_dots must be positive"))
	}
	if n := len(c.Bluetooth.Name); n == 0 || n > maxNameLen {
		errs = append(errs, fmt.Errorf("bluetooth.name must be 1 to %d bytes", maxNameLen))
	}
	if c.Bluetooth.Adapter == "" {
		errs = append(errs, errors.New("bluetooth.adapter is required"))
	}
	// The idle check can't tell a client waiting on a slow print from an idle
	// one, so it must outlast the longest request.
	if c.Bluetooth.IdleTimeout.Duration > 0 && c.Bluetooth.IdleTimeout.Duration <= c.LongestJob() {
		errs = append(errs, fmt.Errorf("bluetooth.idle_timeout must be longer than busy_timeout + status_timeout + print_timeout (%v)", c.LongestJob()))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// LongestJob is the longest a print request can take: waiting its turn, then
// the status check and the job.
func (c Config) LongestJob() time.Duration {
	return c.BusyTimeout.Duration + c.StatusTimeout.Duration + c.PrintTimeout.Duration
}
