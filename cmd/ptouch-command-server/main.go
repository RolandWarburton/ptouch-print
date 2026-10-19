// Command ptouch-command-server queues labels for the board to print, taking
// them over REST and MCP.
//
//	ptouch-command-server [-addr :8080] [-db path]   serve
//	ptouch-command-server token [-client name]       make a token for the board, or a named client
//	ptouch-command-server healthcheck URL            exit 0 if URL answers 200 (for distroless images)
//
// Serving is configured from the environment:
//
//	PTOUCH_BOARD_TOKEN_SHA256  hash of the board's token (required)
//	PTOUCH_CLIENT_TOKENS       name:sha256,... for REST/MCP clients
//	MCP_RESOURCE_URL, MCP_ALLOWED_EMAILS, MCP_OIDC_ISSUER, MCP_OIDC_AUDIENCE
//	                           Dex sign-in for claude.ai, as bookstack-mcp; off unless the first two are set
//	PTOUCH_DB                  database path (default /data/ptouch.sqlite)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/server"
	"github.com/RolandWarburton/ptouch-print-server/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			token(os.Args[2:])
			return
		case "healthcheck":
			healthcheck(os.Args[2:])
			return
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := serve(log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func serve(log *slog.Logger) error {
	dbPath := os.Getenv("PTOUCH_DB")
	if dbPath == "" {
		dbPath = "/data/ptouch.sqlite"
	}
	addr := flag.String("addr", ":8080", "listen address")
	flag.StringVar(&dbPath, "db", dbPath, "database path")
	flag.Parse()

	opts := server.Options{BoardTokenHash: os.Getenv("PTOUCH_BOARD_TOKEN_SHA256")}
	var err error
	if opts.ClientTokens, err = server.ParseTokens(os.Getenv("PTOUCH_CLIENT_TOKENS")); err != nil {
		return fmt.Errorf("PTOUCH_CLIENT_TOKENS: %w", err)
	}
	if os.Getenv("MCP_RESOURCE_URL") != "" || os.Getenv("MCP_ALLOWED_EMAILS") != "" {
		opts.Dex = &server.DexConfig{
			ResourceURL: os.Getenv("MCP_RESOURCE_URL"),
			Issuer:      os.Getenv("MCP_OIDC_ISSUER"),
			Audience:    os.Getenv("MCP_OIDC_AUDIENCE"),
			Allowed:     os.Getenv("MCP_ALLOWED_EMAILS"),
		}
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := server.NewService(st, log)
	handler, err := server.NewHandler(svc, opts, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go svc.Prune(ctx)
	srv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("serving", "addr", *addr, "db", dbPath, "client_tokens", len(opts.ClientTokens), "dex", opts.Dex != nil)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func token(args []string) {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	client := fs.String("client", "", "name of a REST/MCP client; leave out for the board's token")
	fs.Parse(args)
	tok, hash := server.NewToken()
	if *client == "" {
		fmt.Printf("Board token (put it in /etc/ptouch-print-server/token on the board, mode 0600):\n  %s\n\n", tok)
		fmt.Printf("Server setting:\n  PTOUCH_BOARD_TOKEN_SHA256=%s\n", hash)
		return
	}
	fmt.Printf("Token for %s (give it to the client; it is shown only once):\n  %s\n\n", *client, tok)
	fmt.Printf("Add to PTOUCH_CLIENT_TOKENS (comma-separated):\n  %s:%s\n", *client, hash)
}

func healthcheck(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ptouch-command-server healthcheck URL")
		os.Exit(2)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Get(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, res.Status)
		os.Exit(1)
	}
}
