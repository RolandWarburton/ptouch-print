// Command ptouch-print-board runs on the board: it polls the command server
// for labels and prints them on a Brother PT-P700.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/board"
	"github.com/RolandWarburton/ptouch-print/internal/ptouch"
)

func main() {
	configPath := flag.String("config", "/etc/ptouch-print-board/config.toml", "path to the config file")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), "ptouch-print-board runs on the board: it polls the command server for labels and\n"+
			"prints them on a Brother PT-P700. The server URL, token file and poll interval are in\n"+
			"the config file (see deploy/board/config.example.toml).\n\n"+
			"Usage: ptouch-print-board [-config PATH]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := board.LoadConfig(*configPath)
	if err != nil {
		log.Error("loading config", "err", err)
		os.Exit(1)
	}
	token, err := os.ReadFile(cfg.Server.TokenFile)
	if err != nil {
		log.Error("reading the token (make one with ptouch-command-server token)", "err", err)
		os.Exit(1)
	}

	printer := ptouch.New(ptouch.DefaultOpener(), ptouch.Config{
		BusyTimeout:   cfg.BusyTimeout.Duration,
		StatusTimeout: cfg.StatusTimeout.Duration,
		PrintTimeout:  cfg.PrintTimeout.Duration,
		MaxLabelDots:  cfg.MaxLabelDots,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("polling", "server", cfg.Server.URL, "interval", cfg.Server.PollInterval.Duration)
	(&board.Poller{
		ServerURL: cfg.Server.URL,
		Token:     strings.TrimSpace(string(token)),
		Interval:  cfg.Server.PollInterval.Duration,
		Printer:   printer,
		Client:    &http.Client{Timeout: 30 * time.Second},
		Log:       log,
	}).Run(ctx)
	log.Info("stopped")
}
