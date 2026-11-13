// Command assay is stage 03 of the ClearedStack pipeline. It screens each
// candidate against the knockout gates and scores it on the rubric.
//
//	assay eval [-format text|json] FILE   evaluate one observed record
//	assay rank FILE...                    evaluate several and order them
//	assay run                             poll Muster as a worker
//	assay run -standin DIR                run the worker against a stand-in
//	assay version                         print the version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
	"github.com/ClearedStack/assay/internal/errs"
	"github.com/ClearedStack/assay/internal/report"
	"github.com/ClearedStack/assay/tools"
)

// version is the component version from component.json.
const version = "v0.1.0-draft"

// Exit codes.
const (
	exitOK           = 0
	exitFailure      = 1 // could not start or could not read a file
	exitUsage        = 2
	exitNeedsSubtool = 3 // the item was paused with E_NEEDS_SUBTOOL
	exitParse        = 4 // the item was refused with E_PARSE
)

const usage = `assay screens a candidate against the knockout gates and scores it on the rubric.

Usage:
  assay eval [-format text|json] FILE   evaluate one observed record ("-" reads standard input)
  assay rank FILE...                    evaluate several records and print the scored ones in order
  assay run                             poll Muster as a worker (configured by environment)
  assay run -standin DIR                run the worker over the records in DIR, with no Muster
  assay version                         print the version

Exit codes for eval: 0 scored or screened-out, 3 E_NEEDS_SUBTOOL, 4 E_PARSE.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "eval":
		return cmdEval(args[1:], stdin, stdout, stderr)
	case "rank":
		return cmdRank(args[1:], stdout, stderr)
	case "run":
		return cmdRun(ctx, args[1:], stdout, stderr, getenv)
	case "version":
		fmt.Fprintln(stdout, contract.Worker, version)
		return exitOK
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	fmt.Fprint(stderr, usage)
	return exitUsage
}

func newEngine(stderr io.Writer) *engine.Engine {
	set, err := tools.LoadEmbedded()
	if err != nil {
		// The rules are compiled in and tested, so this means a bad build.
		fmt.Fprintln(stderr, "assay: the built-in rules did not load:", err)
		return nil
	}
	return engine.New(set)
}

func readRecord(path string, stdin io.Reader) ([]byte, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	// One byte over the limit is enough for the parser to refuse it.
	return io.ReadAll(io.LimitReader(r, contract.MaxRecordBytes+1))
}

func cmdEval(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 || (*format != "text" && *format != "json") {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	eng := newEngine(stderr)
	if eng == nil {
		return exitFailure
	}
	raw, err := readRecord(fs.Arg(0), stdin)
	if err != nil {
		fmt.Fprintln(stderr, "assay: the record could not be read")
		return exitFailure
	}
	out := eng.Evaluate(raw)
	if *format == "json" {
		_, err = stdout.Write(contract.Encode(out))
	} else {
		err = report.Text(stdout, out)
	}
	if err != nil {
		return exitFailure
	}
	if out.Error != nil {
		if out.Error.Code == errs.NeedsSubtool {
			return exitNeedsSubtool
		}
		return exitParse
	}
	return exitOK
}

func cmdRank(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	eng := newEngine(stderr)
	if eng == nil {
		return exitFailure
	}
	var scored, others []*contract.Outcome
	for _, path := range args {
		raw, err := readRecord(path, nil)
		if path == "-" || err != nil {
			fmt.Fprintln(stderr, "assay: a record could not be read")
			return exitFailure
		}
		out := eng.Evaluate(raw)
		if out.State == contract.StateScored {
			scored = append(scored, out)
		} else {
			others = append(others, out)
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return engine.Compare(scored[i], scored[j]) < 0 })
	sort.SliceStable(others, func(i, j int) bool { return others[i].ItemID < others[j].ItemID })

	fmt.Fprintf(stdout, "%-5s%-28s%-7s%-8s%s\n", "Rank", "Item", "Total", "Rec.", "Tie-breakers")
	for i, o := range scored {
		ties := ""
		for j, t := range o.TieBreakers {
			if j > 0 {
				ties += " "
			}
			ties += fmt.Sprint(t.Value)
		}
		fmt.Fprintf(stdout, "%-5d%-28s%-7d%-8s%s\n", i+1, o.ItemID, *o.Total, o.Recommendation, ties)
	}
	if len(others) > 0 {
		fmt.Fprintln(stdout, "\nNot ranked")
		for _, o := range others {
			id, state := o.ItemID, o.State
			if id == "" {
				id = "(unreadable)"
			}
			if state == "" {
				state = string(o.Error.Code)
			}
			fmt.Fprintf(stdout, "     %-28s%s\n", id, state)
		}
	}
	return exitOK
}
