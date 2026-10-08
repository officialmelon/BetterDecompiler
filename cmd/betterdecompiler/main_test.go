package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseInterspersedFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	o := fs.String("o", "", "")
	q := fs.Bool("q", false, "")
	pos, err := parse(fs, []string{"a.lua", "-o", "out", "b.lua", "-q", "--", "-c.lua"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"a.lua", "b.lua", "-c.lua"}) || *o != "out" || !*q {
		t.Fatalf("pos=%v o=%q q=%v", pos, *o, *q)
	}
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	must := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x()"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, "src", "a.lua"))
	must(filepath.Join(dir, "src", "deep", "b.luau"))
	must(filepath.Join(dir, "src", "notes.md"))
	single := filepath.Join(dir, "src", "a.lua")

	jobs, err := collect([]string{single}, "", false)
	if err != nil || len(jobs) != 1 || jobs[0].out != "" {
		t.Fatalf("single to stdout: %+v %v", jobs, err)
	}
	jobs, _ = collect([]string{single}, filepath.Join(dir, "o.lua"), false)
	if jobs[0].out != filepath.Join(dir, "o.lua") {
		t.Fatalf("single to file: %+v", jobs)
	}
	jobs, err = collect([]string{filepath.Join(dir, "src")}, filepath.Join(dir, "out"), false)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("dir: %+v %v", jobs, err)
	}
	want := map[string]bool{filepath.Join(dir, "out", "a.lua"): true, filepath.Join(dir, "out", "deep", "b.luau"): true}
	for _, j := range jobs {
		if !want[j.out] {
			t.Fatalf("unexpected output path %s", j.out)
		}
	}
	if _, err := collect([]string{filepath.Join(dir, "src")}, "", false); err == nil {
		t.Fatal("directory without -o must be rejected")
	}
	jobs, _ = collect([]string{filepath.Join(dir, "src")}, "", true)
	for _, j := range jobs {
		if j.in != j.out {
			t.Fatal("in-place must overwrite inputs")
		}
	}
}
