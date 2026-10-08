package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/config"
	"github.com/officialmelon/betterdecompiler/internal/llm"
)

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt, def string) string {
	fmt.Print(prompt + " ")
	line, _ := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return strings.ToLower(line[:1]) + line[1:]
}

// askSecret reads a line without echo where the terminal supports it.
func askSecret(prompt string) string {
	fmt.Print(prompt + " ")
	hidden := false
	if runtime.GOOS != "windows" && isTerminal(os.Stdin) {
		cmd := exec.Command("stty", "-echo")
		cmd.Stdin = os.Stdin
		hidden = cmd.Run() == nil
	}
	line, _ := stdin.ReadString('\n')
	if hidden {
		cmd := exec.Command("stty", "echo")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
		fmt.Println()
	}
	return strings.TrimSpace(line)
}

func runSetup(args []string) error {
	fs := newFlagSet("setup", "setup [--config PATH]")
	path := fs.String("config", config.DefaultPath(), "config file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	fmt.Println("BetterDecompiler setup — pick the AI that names your variables. Your key stays on this computer.")
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for i, p := range llm.Presets {
		note := p.Description
		if p.EnvKey() != "" {
			note += "  [key found in " + firstEnv(p) + "]"
		}
		fmt.Fprintf(w, "  %2d) %s\t%s\n", i+1, p.Name, note)
	}
	fmt.Fprintf(w, "  %2d) %s\t%s\n", len(llm.Presets)+1, llm.Offline, "No AI: names inferred from Roblox API usage (free, instant)")
	w.Flush()
	choice := ask(fmt.Sprintf("\nChoice [1-%d, default 1]:", len(llm.Presets)+1), "1")
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(llm.Presets)+1 {
		return fmt.Errorf("invalid choice %q", choice)
	}
	if n == len(llm.Presets)+1 {
		cfg.Provider = llm.Offline
		if err := config.Save(*path, cfg); err != nil {
			return err
		}
		fmt.Println("Saved. The offline renamer will be used:", *path)
		return nil
	}
	p := llm.Presets[n-1]
	user := cfg.Providers[p.Name]
	if p.NeedsKey {
		if env := firstEnv(p); env != "" && ask(fmt.Sprintf("Use the key from $%s? (stores a reference, not the key) [Y/n]", env), "y") == "y" {
			user.APIKey = "env:" + env
		} else {
			fmt.Printf("Get a key at %s\n", p.KeyURL)
			key := askSecret("Paste your API key:")
			if key == "" {
				return fmt.Errorf("no key entered")
			}
			user.APIKey = key
		}
	} else {
		fmt.Printf("%s runs locally: make sure it is running (%s).\n", p.Name, p.KeyURL)
		if url := ask(fmt.Sprintf("Server URL [%s]:", p.BaseURL), ""); url != "" {
			user.BaseURL = url
		}
	}
	defModel := p.Model
	if user.Model != "" {
		defModel = user.Model
	}
	if m := ask(fmt.Sprintf("Model [%s]:", defModel), ""); m != "" {
		user.Model = m
	}
	cfg.SetProvider(p.Name, user)
	cfg.Provider = p.Name

	if ask("Test the connection now? [Y/n]", "y") == "y" {
		testCfg := *cfg
		clients, _, err := testCfg.Build()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var models []string
			if models, err = clients[p.Name].ListModels(ctx); err == nil {
				fmt.Printf("  OK: connected, %d models available.\n", len(models))
			}
		}
		if err != nil {
			fmt.Printf("  Connection test failed: %v\n", err)
			if ask("Save anyway? [y/N]", "n") != "y" {
				return fmt.Errorf("setup cancelled")
			}
		}
	}
	if err := config.Save(*path, cfg); err != nil {
		return err
	}
	fmt.Println("Saved to", *path)
	fmt.Println("Start the server with `betterdecompiler` or clean a file with `betterdecompiler clean script.lua`.")
	return nil
}

func firstEnv(p llm.Preset) string {
	for _, k := range p.KeyEnv {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return ""
}

func runProviders(args []string) error {
	fs := newFlagSet("providers", "providers [--config PATH]")
	var c common
	c.register(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnv()
	_, chain, buildErr := cfg.Build()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER\tREADY\tMODEL\tDESCRIPTION")
	for _, p := range cfg.List() {
		ready := "no"
		switch {
		case p.Ready && p.Local:
			ready = "local"
		case p.Ready:
			ready = "yes"
		case p.KeyEnv != "":
			ready = "no (set " + p.KeyEnv + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, ready, p.Model, p.Description)
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", llm.Offline, "yes", "-", "No AI: names inferred from Roblox API usage")
	w.Flush()
	if buildErr != nil {
		fmt.Println("\nconfiguration error:", buildErr)
	} else {
		fmt.Println("\nactive chain:", strings.Join(chain, " -> "))
	}
	return nil
}

func runModels(args []string) error {
	fs := newFlagSet("models", "models [-p provider]")
	var c common
	c.register(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnv()
	if c.provider != "" {
		cfg.Provider = c.provider
	}
	clients, chain, err := cfg.Build()
	if err != nil {
		return err
	}
	name := chain[0]
	if name == llm.Offline {
		return fmt.Errorf("no AI provider configured; run `betterdecompiler setup` or pass -p")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := clients[name].ListModels(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s (current: %s)\n", name, clients[name].Model())
	for _, m := range models {
		fmt.Println(m)
	}
	return nil
}

func runConfig(args []string) error {
	fs := newFlagSet("config", "config [path|show]")
	path := fs.String("config", config.DefaultPath(), "config file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	sub := "show"
	if len(pos) > 0 {
		sub = pos[0]
	}
	switch sub {
	case "path":
		fmt.Println(*path)
		return nil
	case "show":
		cfg, err := config.Load(*path)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "#", *path)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cfg.Redacted())
	}
	return fmt.Errorf("unknown config command %q (want path or show)", sub)
}
