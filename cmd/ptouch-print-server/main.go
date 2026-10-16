// Command ptouch-print-server exposes a Brother PT-P700 over Bluetooth LE.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/RolandWarburton/ptouch-print-server/internal/api"
	"github.com/RolandWarburton/ptouch-print-server/internal/config"
	"github.com/RolandWarburton/ptouch-print-server/internal/gatt"
	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
)

func main() {
	configPath := flag.String("config", "/etc/ptouch-print-server/config.toml", "path to the config file")
	// The systemd unit's StateDirectory.
	keyPath := flag.String("key", "/var/lib/ptouch-print-server/api-key", "path to the API key, generated if missing")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("loading config", "err", err)
		os.Exit(1)
	}

	printer := ptouch.New(ptouch.DefaultOpener(), ptouch.Config{
		BusyTimeout:   cfg.BusyTimeout.Duration,
		StatusTimeout: cfg.StatusTimeout.Duration,
		PrintTimeout:  cfg.PrintTimeout.Duration,
		MaxLabelDots:  cfg.MaxLabelDots,
	})
	key, err := apiKey(*keyPath)
	if err != nil {
		log.Error("API key", "err", err)
		os.Exit(1)
	}
	svc := api.New(printer, key, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = gatt.Serve(ctx, gatt.Config{
		Adapter:     cfg.Bluetooth.Adapter,
		Name:        cfg.Bluetooth.Name,
		IdleTimeout: cfg.Bluetooth.IdleTimeout.Duration,
		// Let a label that is already printing finish so its result is logged.
		ShutdownTimeout: cfg.LongestJob(),
	}, svc, log)
	if err != nil {
		log.Error("bluetooth", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

// apiKey returns the API key stored at path, generating it on first start.
func apiKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		b = []byte(rand.Text())
		err = os.WriteFile(path, b, 0o600)
	}
	return strings.TrimSpace(string(b)), err
}
