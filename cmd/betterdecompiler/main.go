// Command betterdecompiler cleans up decompiled Roblox Luau with the AI
// provider of your choice (or fully offline).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/officialmelon/betterdecompiler/internal/config"
	"github.com/officialmelon/betterdecompiler/internal/engine"
)

// version is set at build time with -ldflags "-X main.version=v2.0.0".
var version = "dev"

const usage = `BetterDecompiler %s - turn decompiled Roblox Luau into readable code.

Usage:
  betterdecompiler [serve]             start the local server (web UI + executor API)
  betterdecompiler clean [files|dirs]  clean files, folders or stdin from the terminal
  betterdecompiler setup               choose an AI provider and save your API key
  betterdecompiler providers           list supported providers and which are ready
  betterdecompiler models [-p name]    list the models your key can use
  betterdecompiler config [path|show]  show where settings live / print them (keys masked)
  betterdecompiler version

Common flags:
  -p, --provider NAME   anthropic, openai, gemini, openrouter, groq, deepseek, mistral,
                        xai, ollama, lmstudio, offline, or a custom provider
  -m, --model NAME      model to use (default depends on the provider)
  --mode MODE           rename (default: fast, behavior-preserving), rewrite, offline
  --comments LEVEL      none, light (default), detailed
  --config PATH         config file (default: %s)

Run "betterdecompiler <command> -h" for command-specific flags.
Docs: https://github.com/officialmelon/BetterDecompiler
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve", "server", "start":
		err = runServe(args, len(os.Args) == 1)
	case "clean", "c":
		err = runClean(args)
	case "setup", "init", "configure":
		err = runSetup(args)
	case "providers":
		err = runProviders(args)
	case "models":
		err = runModels(args)
	case "config":
		err = runConfig(args)
	case "version", "--version", "-v":
		fmt.Println("betterdecompiler", version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version, config.DefaultPath())
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, usage, version, config.DefaultPath())
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		if len(os.Args) == 1 && isTerminal(os.Stdin) {
			// Launched by double-click: keep the window open so the error is readable.
			fmt.Fprint(os.Stderr, "\nPress Enter to exit...")
			_, _ = fmt.Fscanln(os.Stdin)
		}
		os.Exit(1)
	}
}

// common holds flags shared by most commands.
type common struct {
	configPath string
	provider   string
	model      string
	mode       string
	comments   string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.configPath, "config", config.DefaultPath(), "config file")
	fs.StringVar(&c.provider, "provider", "", "AI provider")
	fs.StringVar(&c.provider, "p", "", "AI provider (shorthand)")
	fs.StringVar(&c.model, "model", "", "model name")
	fs.StringVar(&c.model, "m", "", "model name (shorthand)")
	fs.StringVar(&c.mode, "mode", "", "rename, rewrite or offline")
	fs.StringVar(&c.comments, "comments", "", "none, light or detailed")
}

// load reads the config, applies environment and flag overrides and builds
// the engine.
func (c *common) load() (*config.Config, *engine.Engine, error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, nil, err
	}
	cfg.ApplyEnv()
	if c.provider != "" {
		cfg.Provider = c.provider
	}
	if c.mode != "" {
		cfg.Mode = c.mode
	}
	if c.comments != "" {
		cfg.Comments = c.comments
	}
	clients, chain, err := cfg.Build()
	if err != nil {
		return nil, nil, err
	}
	if c.model != "" && chain[0] != "offline" {
		p := cfg.Providers[chain[0]]
		p.Model = c.model
		cfg.SetProvider(chain[0], p)
		if clients, chain, err = cfg.Build(); err != nil {
			return nil, nil, err
		}
	}
	return cfg, engine.New(cfg.EngineSettings(version), clients, chain, cfg.NewCache()), nil
}

// parse parses flags that may appear before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func newFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: betterdecompiler %s\n\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
}
