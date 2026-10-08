package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/officialmelon/betterdecompiler/internal/engine"
)

type job struct {
	in, out string // out "" = stdout
	rel     string
}

func runClean(args []string) error {
	fls := newFlagSet("clean", "clean [flags] [file|dir|-]...")
	var c common
	c.register(fls)
	out := fls.String("o", "", "output file, or output directory for several inputs")
	inPlace := fls.Bool("in-place", false, "overwrite input files")
	noHeader := fls.Bool("no-header", false, "do not add the informational header comment")
	noCache := fls.Bool("no-cache", false, "ignore cached results")
	jobs := fls.Int("j", 4, "files to process in parallel")
	asJSON := fls.Bool("json", false, "print results as JSON (one object per line)")
	quiet := fls.Bool("q", false, "no progress output")
	inputs, err := parse(fls, args)
	if err != nil {
		return err
	}
	_, eng, err := c.load()
	if err != nil {
		return err
	}
	opts := engine.Options{NoCache: *noCache}
	if *noHeader {
		f := false
		opts.Header = &f
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// stdin -> stdout (or -o file)
	if len(inputs) == 0 || (len(inputs) == 1 && inputs[0] == "-") {
		if len(inputs) == 0 && isTerminal(os.Stdin) {
			fls.Usage()
			return errors.New("no input: pass files or directories, or pipe code into stdin")
		}
		src, err := readAll(os.Stdin)
		if err != nil {
			return err
		}
		res, err := eng.Clean(ctx, src, opts)
		if err != nil {
			return err
		}
		return emit(res, *out, *asJSON, "stdin")
	}

	list, err := collect(inputs, *out, *inPlace)
	if err != nil {
		return err
	}
	if *jobs < 1 {
		*jobs = 1
	}
	var (
		mu     sync.Mutex
		failed int
		done   int
		wg     sync.WaitGroup
		queue  = make(chan job)
		start  = time.Now()
	)
	for w := 0; w < *jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				t0 := time.Now()
				data, err := os.ReadFile(j.in)
				var res *engine.Result
				if err == nil {
					res, err = eng.Clean(ctx, string(data), opts)
				}
				if err == nil {
					err = emit(res, j.out, *asJSON, j.in)
				}
				mu.Lock()
				done++
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "[%d/%d] %s: error: %v\n", done, len(list), j.rel, err)
				} else if !*quiet && j.out != "" {
					note := fmt.Sprintf("%s, %d names", res.Provider, res.Renamed)
					if res.Cached {
						note += ", cached"
					}
					for _, w := range res.Warnings {
						note += "; warning: " + w
					}
					fmt.Fprintf(os.Stderr, "[%d/%d] %s -> %s (%.1fs, %s)\n", done, len(list), j.rel, j.out, time.Since(t0).Seconds(), note)
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range list {
		queue <- j
	}
	close(queue)
	wg.Wait()
	if !*quiet && len(list) > 1 {
		st := eng.Stats()
		fmt.Fprintf(os.Stderr, "done: %d files in %.1fs (%d failed, %d cached, %d AI calls, %d in / %d out tokens)\n",
			len(list), time.Since(start).Seconds(), failed, st.CacheHits, st.AICalls, st.InputTokens, st.OutputTokens)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(list))
	}
	return nil
}

func isLua(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".lua" || ext == ".luau" || ext == ".txt"
}

// collect expands inputs into jobs and decides where each result goes.
func collect(inputs []string, out string, inPlace bool) ([]job, error) {
	var list []job
	multi := len(inputs) > 1
	for _, in := range inputs {
		st, err := os.Stat(in)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			list = append(list, job{in: in, rel: filepath.Base(in)})
			continue
		}
		multi = true
		err = filepath.WalkDir(in, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isLua(p) {
				rel, _ := filepath.Rel(in, p)
				if len(inputs) > 1 {
					rel = filepath.Join(filepath.Base(in), rel)
				}
				list = append(list, job{in: p, rel: rel})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(list) == 0 {
		return nil, errors.New("no .lua or .luau files found")
	}
	switch {
	case inPlace:
		for i := range list {
			list[i].out = list[i].in
		}
	case out != "" && !multi:
		if st, err := os.Stat(out); err == nil && st.IsDir() {
			list[0].out = filepath.Join(out, list[0].rel)
		} else {
			list[0].out = out
		}
	case out != "":
		for i := range list {
			list[i].out = filepath.Join(out, list[i].rel)
		}
	case multi:
		return nil, errors.New("several inputs: use -o DIR to choose an output directory, or --in-place")
	}
	return list, nil
}

func emit(res *engine.Result, out string, asJSON bool, name string) error {
	var data []byte
	if asJSON {
		v := struct {
			File string `json:"file"`
			*engine.Result
		}{name, res}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		data = append(b, '\n')
	} else {
		data = []byte(res.Source)
	}
	if out == "" {
		_, err := os.Stdout.Write(data)
		if !asJSON {
			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}
