package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/tools"
)

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	updateGoldens := fs.Bool("update", false, "rewrite golden files from the current rules before checking")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		say(stderr, usage)
		return exitUsage
	}
	dir := "tools"
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		say(stderr, "assay: the rules could not be read")
		return exitFailure
	}

	rootFS := os.DirFS(dir)
	if *updateGoldens {
		set, err := tools.Load(rootFS)
		if err != nil {
			say(stderr, "assay: the rules did not load:", err)
			return exitFailure
		}
		subPath, err := tools.SubToolPath(rootFS)
		if err != nil {
			say(stderr, "assay: the rules did not load:", err)
			return exitFailure
		}
		subDir := filepath.Join(dir, filepath.FromSlash(subPath))
		pairs, _, err := tools.FindFixtures(os.DirFS(subDir))
		if err != nil {
			say(stderr, "assay: the fixtures could not be read")
			return exitFailure
		}
		eng := engine.New(set)
		for _, fx := range pairs {
			inPath := filepath.Join(subDir, filepath.FromSlash(fx.Input))
			raw, err := os.ReadFile(inPath)
			if err != nil {
				say(stderr, "assay: a fixture could not be read")
				return exitFailure
			}
			got := contract.Encode(eng.Evaluate(raw))
			goldPath := filepath.Join(subDir, filepath.FromSlash(fx.Golden))
			if err := os.WriteFile(goldPath, got, 0o644); err != nil {
				say(stderr, "assay: a golden file could not be written")
				return exitFailure
			}
		}
	}

	sum, problems, err := tools.Verify(os.DirFS(dir))
	if err != nil {
		say(stderr, "assay: the rules did not load:", err)
		return exitFailure
	}
	if len(problems) > 0 {
		for _, p := range problems {
			say(stderr, "assay:", p)
		}
		return exitFailure
	}
	sayf(stdout, "ok: %d categories, %d rules, %d fixtures\n", sum.Categories, sum.Rules, sum.Fixtures)
	return exitOK
}
