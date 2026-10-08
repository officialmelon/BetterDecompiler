package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/config"
	"github.com/officialmelon/betterdecompiler/internal/llm"
	"github.com/officialmelon/betterdecompiler/internal/server"
)

const clientURL = "https://github.com/officialmelon/BetterDecompiler/releases/latest/download/betterdecompiler.lua"

func runServe(args []string, bare bool) error {
	fs := newFlagSet("serve", "serve [flags]")
	var c common
	c.register(fs)
	listen := fs.String("listen", "", "address to listen on (default "+config.DefaultListen+")")
	token := fs.String("token", "", "require this token from clients (Authorization: Bearer ...)")
	anyHost := fs.Bool("allow-any-host", false, "answer requests for any Host (needed for LAN or reverse-proxy use)")
	quiet := fs.Bool("quiet", false, "do not log requests")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if bare && isTerminal(os.Stdin) && needsSetup(c.configPath) {
		fmt.Println("No AI provider is configured yet.")
		if ask("Set one up now? (you can also use the offline renamer) [Y/n]", "y") == "y" {
			if err := runSetup([]string{"--config", c.configPath}); err != nil {
				return err
			}
			fmt.Println()
		}
	}
	cfg, eng, err := c.load()
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *token != "" {
		cfg.Token = *token
	}
	addr := cfg.ListenAddr()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if !isLoopbackListen(host) && cfg.Token == "" {
		return fmt.Errorf("listening on %s would let other devices use your API key; set a token with --token (or BD_TOKEN)", host)
	}
	var logger *log.Logger
	if !*quiet {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}
	srv := &server.Server{Engine: eng, Providers: cfg.List(), Token: cfg.Token, Version: version, Log: logger,
		AllowAnyHost: *anyHost || !isLoopbackListen(host)}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}

	listeners, err := listen4and6(host, port)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "Only one usage") {
			return fmt.Errorf("%w\nanother program is using port %s (on macOS, AirPlay Receiver uses 5000).\n"+
				"Try: betterdecompiler serve --listen 127.0.0.1:5050  (and point your client at that port)", err, port)
		}
		return err
	}
	printBanner(cfg, eng.Chain(), host, port)

	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func(ln net.Listener) { errc <- hs.Serve(ln) }(ln)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-sig:
		fmt.Fprintln(os.Stderr, "\nshutting down...")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(ctx)
}

func isLoopbackListen(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listen4and6 binds the address. For localhost it binds both 127.0.0.1 and
// ::1, because clients resolve "localhost" to either.
func listen4and6(host, port string) ([]net.Listener, error) {
	if host == "localhost" || host == "127.0.0.1" {
		ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			return nil, err
		}
		lns := []net.Listener{ln}
		if ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", port)); err == nil {
			lns = append(lns, ln6)
		}
		return lns, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	return []net.Listener{ln}, nil
}

func printBanner(cfg *config.Config, chain []string, host, port string) {
	shown := host
	if host == "127.0.0.1" || host == "" || host == "0.0.0.0" || host == "::" {
		shown = "localhost"
	}
	base := "http://" + net.JoinHostPort(shown, port)
	var provs []string
	for _, name := range chain {
		if name == llm.Offline {
			provs = append(provs, "offline renamer")
			continue
		}
		for _, p := range cfg.List() {
			if p.Name == name {
				provs = append(provs, fmt.Sprintf("%s (%s)", name, p.Model))
			}
		}
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "rename"
	}
	if len(chain) == 1 && chain[0] == llm.Offline {
		mode = "offline"
	}
	fmt.Printf(`
  BetterDecompiler %s

  Web UI     %s
  API        POST %s/v1/clean   (v1 clients and Dex: %s/fix_script)
  Provider   %s
  Mode       %s
`, version, base, base, base, strings.Join(provs, " -> "), mode)
	if cfg.Token != "" {
		fmt.Println("  Auth       token required (Authorization: Bearer <token>)")
	}
	if !isLoopbackListen(host) {
		fmt.Println("  Note       reachable from other devices; clients must send the token.")
	}
	if len(chain) == 1 && chain[0] == llm.Offline {
		fmt.Println("\n  No API key configured, so names come from the offline renamer.")
		fmt.Println("  Run `betterdecompiler setup` (or set e.g. ANTHROPIC_API_KEY) to use an AI.")
	}
	fmt.Printf(`
  In your executor:
    loadstring(game:HttpGet("%s"))()

  Press Ctrl+C to stop.

`, clientURL)
}

// needsSetup reports whether nothing at all is configured.
func needsSetup(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return false
	}
	if os.Getenv("BD_PROVIDER") != "" {
		return false
	}
	for _, p := range llm.Presets {
		if p.NeedsKey && p.EnvKey() != "" {
			return false
		}
	}
	return true
}
