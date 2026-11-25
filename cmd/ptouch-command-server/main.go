// Command ptouch-command-server queues labels for the printers' boards to print, taking
// them over REST and MCP. Run it with -h for its commands and settings.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RolandWarburton/ptouch-print/internal/server"
	"github.com/RolandWarburton/ptouch-print/internal/store"
	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const usage = `ptouch-command-server queues labels for the printers' boards to print, taking them over REST and MCP.

Usage:
  ptouch-command-server [-addr :8080] [-db PATH]   serve
  ptouch-command-server token -printer NAME [-family FAMILY]
                                                   make a token for a printer's board
  ptouch-command-server token -client NAME         make a token for a REST/MCP client
  ptouch-command-server healthcheck URL            exit 0 if URL answers 200 (for container health checks)
  ptouch-command-server help                       show this

Serving is configured from the environment:
  PRINT_PRINTERS             name:family:hash,name:family:hash for the printers (from "token -printer NAME");
                             the family is ptouch or lw450
  PTOUCH_BOARD_TOKEN_SHA256  hash of the board's token for a P-touch called "ptouch", from before there could be
                             several printers; same as ptouch:ptouch:hash in PRINT_PRINTERS. One of the two is required
  PRINT_CLIENT_TOKENS        name:hash,name:hash for REST/MCP clients (from "token -client NAME")
  PRINT_DB                   database path, same as -db (default /data/ptouch.sqlite)

  Dex sign-in for claude.ai, on when MCP_RESOURCE_URL or MCP_ALLOWED_EMAILS is set:
  MCP_OIDC_ISSUER            Dex's URL (required with Dex)
  MCP_RESOURCE_URL           public URL of /mcp, e.g. https://ptouch.example.com/mcp
  MCP_ALLOWED_EMAILS         comma-separated emails who may print
  MCP_OIDC_AUDIENCE          the Dex client (default ptouch-mcp)

  The web UI at /, off unless WEB_UI is true. People sign in through Dex, so it needs Dex set up:
  WEB_UI                     true to serve it
  PRINT_WEB_OIDC_SECRET      the secret of Dex client ptouch-web (required with WEB_UI)

Flags for serving:
`

func main() {
	dbPath := os.Getenv("PRINT_DB")
	if dbPath == "" {
		dbPath = "/data/ptouch.sqlite"
	}
	addr := flag.String("addr", ":8080", "listen address")
	flag.StringVar(&dbPath, "db", dbPath, "database path")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			token(os.Args[2:])
			return
		case "healthcheck":
			healthcheck(os.Args[2:])
			return
		case "help":
			flag.CommandLine.SetOutput(os.Stdout)
			flag.Usage()
			return
		}
	}
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", flag.Arg(0))
		flag.Usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := serve(log, *addr, dbPath); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func serve(log *slog.Logger, addr, dbPath string) error {
	printers, err := server.ParsePrinters(os.Getenv("PRINT_PRINTERS"), os.Getenv("PTOUCH_BOARD_TOKEN_SHA256"))
	if err != nil {
		return fmt.Errorf("PRINT_PRINTERS: %w", err)
	}
	var opts server.Options
	if opts.ClientTokens, err = server.ParseTokens(os.Getenv("PRINT_CLIENT_TOKENS")); err != nil {
		return fmt.Errorf("PRINT_CLIENT_TOKENS: %w", err)
	}
	if os.Getenv("MCP_RESOURCE_URL") != "" || os.Getenv("MCP_ALLOWED_EMAILS") != "" {
		opts.Dex = &server.DexConfig{
			ResourceURL: os.Getenv("MCP_RESOURCE_URL"),
			Issuer:      os.Getenv("MCP_OIDC_ISSUER"),
			Audience:    os.Getenv("MCP_OIDC_AUDIENCE"),
			Allowed:     os.Getenv("MCP_ALLOWED_EMAILS"),
		}
	}
	if s := os.Getenv("WEB_UI"); s != "" {
		if opts.WebUI, err = strconv.ParseBool(s); err != nil {
			return fmt.Errorf("WEB_UI must be true or false: %w", err)
		}
	}
	opts.WebSecret = os.Getenv("PRINT_WEB_OIDC_SECRET")

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	svc := server.NewService(st, printers, log)
	handler, err := server.NewHandler(svc, opts, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go svc.Prune(ctx)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("serving", "addr", addr, "db", dbPath, "printers", len(printers), "client_tokens", len(opts.ClientTokens), "dex", opts.Dex != nil, "web_ui", opts.WebUI)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func token(args []string) {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	client := fs.String("client", "", "name of a REST/MCP client")
	printer := fs.String("printer", "", "name of a printer, for its board's token")
	family := fs.String("family", wire.FamilyPTouch, "the printer's family: ptouch or lw450")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: ptouch-command-server token -printer NAME [-family FAMILY] | -client NAME\n\n"+
			"Makes a random token and prints it with its hash. The token goes to the board or client;\n"+
			"the hash goes in the server's environment. The token is shown only this once.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() > 0 || (*client == "") == (*printer == "") {
		fs.Usage()
		os.Exit(2)
	}
	tok, hash := server.NewToken()
	if *printer != "" {
		p, err := server.NewPrinter(*printer, *family, hash)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("Board token for %s (put it in the board's settings):\n  %s\n\n", p.Name, tok)
		fmt.Printf("Add to PRINT_PRINTERS (comma-separated):\n  %s:%s:%s\n", p.Name, p.Family, p.TokenHash)
		return
	}
	fmt.Printf("Token for %s (give it to the client; it is shown only once):\n  %s\n\n", *client, tok)
	fmt.Printf("Add to PRINT_CLIENT_TOKENS (comma-separated):\n  %s:%s\n", *client, hash)
}

func healthcheck(args []string) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "Usage: ptouch-command-server healthcheck URL\n\nExits 0 if URL answers 200.")
		if len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
			os.Exit(0)
		}
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
