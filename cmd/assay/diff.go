package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/engine"
)

type diffReport struct {
	Evaluated int        `json:"evaluated"`
	Changed   int        `json:"changed"`
	Unchanged int        `json:"unchanged"`
	Items     []diffItem `json:"items"`
}

type diffItem struct {
	ID             string       `json:"id"`
	Category       string       `json:"category,omitempty"`
	State          *stringPair  `json:"state,omitempty"`
	Score          *intPair     `json:"score,omitempty"`
	Recommendation *stringPair  `json:"recommendation,omitempty"`
	SplitFlag      *boolPair    `json:"split_flag,omitempty"`
	Rank           *intPair     `json:"rank,omitempty"`
	Gates          []namedShift `json:"gates,omitempty"`
	Factors        []namedShift `json:"factors,omitempty"`
}

type stringPair struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type intPair struct {
	Before *int `json:"before"`
	After  *int `json:"after"`
}

type boolPair struct {
	Before *bool `json:"before"`
	After  *bool `json:"after"`
}

type namedShift struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func cmdDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	beforeDir := fs.String("before", "", "rule directory for the baseline (defaults to built-in rules)")
	afterDir := fs.String("after", "", "rule directory to compare against the baseline")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *afterDir == "" || fs.NArg() == 0 || (*format != "text" && *format != "json") {
		say(stderr, usage)
		return exitUsage
	}
	for _, path := range fs.Args() {
		if path == "-" {
			say(stderr, "assay: diff reads files, not standard input")
			return exitUsage
		}
	}

	beforeEng := loadEngine(*beforeDir, stderr)
	if beforeEng == nil {
		return exitFailure
	}
	afterEng := loadEngine(*afterDir, stderr)
	if afterEng == nil {
		return exitFailure
	}

	type evaluatedPair struct {
		index  int
		id     string
		before *contract.Outcome
		after  *contract.Outcome
	}
	records := make([]evaluatedPair, 0, fs.NArg())
	var beforeScored, afterScored []*contract.Outcome

	for i, path := range fs.Args() {
		raw, err := readRecord(path, nil)
		if err != nil {
			say(stderr, "assay: a record could not be read")
			return exitFailure
		}
		bOut := beforeEng.Evaluate(raw)
		aOut := afterEng.Evaluate(raw)
		id := aOut.ItemID
		if id == "" {
			id = bOut.ItemID
		}
		if id == "" {
			id = fmt.Sprintf("(unreadable-%d)", i+1)
		}
		records = append(records, evaluatedPair{index: i, id: id, before: bOut, after: aOut})
		if bOut.State == contract.StateScored {
			beforeScored = append(beforeScored, bOut)
		}
		if aOut.State == contract.StateScored {
			afterScored = append(afterScored, aOut)
		}
	}

	beforeRanks := rankPositions(beforeScored)
	afterRanks := rankPositions(afterScored)

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].id != records[j].id {
			return records[i].id < records[j].id
		}
		return records[i].index < records[j].index
	})

	rep := diffReport{
		Evaluated: len(records),
		Items:     []diffItem{},
	}

	for _, rec := range records {
		item, changed := compareOutcomes(rec.id, rec.before, rec.after, beforeRanks[rec.before], afterRanks[rec.after])
		if changed {
			rep.Changed++
			rep.Items = append(rep.Items, item)
		} else {
			rep.Unchanged++
		}
	}

	if *format == "json" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return exitFailure
		}
		if _, err := stdout.Write(buf.Bytes()); err != nil {
			return exitFailure
		}
		return exitOK
	}

	sayf(stdout, "Evaluated: %d, Changed: %d, Unchanged: %d\n", rep.Evaluated, rep.Changed, rep.Unchanged)
	if len(rep.Items) == 0 {
		return exitOK
	}
	say(stdout, "")
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	say(tw, "Item\tField\tBefore\tAfter")
	for _, it := range rep.Items {
		if it.State != nil {
			sayf(tw, "%s\tstate\t%s\t%s\n", it.ID, it.State.Before, it.State.After)
		}
		if it.Score != nil {
			sayf(tw, "%s\tscore\t%s\t%s\n", it.ID, fmtIntPtr(it.Score.Before), fmtIntPtr(it.Score.After))
		}
		if it.Recommendation != nil {
			sayf(tw, "%s\trecommendation\t%s\t%s\n", it.ID, dashEmpty(it.Recommendation.Before), dashEmpty(it.Recommendation.After))
		}
		if it.SplitFlag != nil {
			sayf(tw, "%s\tsplit_flag\t%s\t%s\n", it.ID, fmtBoolPtr(it.SplitFlag.Before), fmtBoolPtr(it.SplitFlag.After))
		}
		if it.Rank != nil {
			sayf(tw, "%s\trank\t%s\t%s\n", it.ID, fmtIntPtr(it.Rank.Before), fmtIntPtr(it.Rank.After))
		}
		for _, g := range it.Gates {
			sayf(tw, "%s\tgate.%s\t%s\t%s\n", it.ID, g.ID, g.Before, g.After)
		}
		for _, f := range it.Factors {
			sayf(tw, "%s\tfactor.%s\t%s\t%s\n", it.ID, f.ID, f.Before, f.After)
		}
	}
	if err := tw.Flush(); err != nil {
		return exitFailure
	}
	return exitOK
}

func rankPositions(scored []*contract.Outcome) map[*contract.Outcome]*int {
	ordered := append([]*contract.Outcome(nil), scored...)
	sort.SliceStable(ordered, func(i, j int) bool { return engine.Compare(ordered[i], ordered[j]) < 0 })
	pos := make(map[*contract.Outcome]*int, len(ordered))
	for i, o := range ordered {
		r := i + 1
		pos[o] = &r
	}
	return pos
}

func outcomeStateLabel(o *contract.Outcome) string {
	if o.Error != nil {
		if o.Error.Where != "" {
			return fmt.Sprintf("%s(%s:%s)", o.Error.Code, o.Error.Reason, o.Error.Where)
		}
		return fmt.Sprintf("%s(%s)", o.Error.Code, o.Error.Reason)
	}
	return o.State
}

func compareOutcomes(id string, b, a *contract.Outcome, bRank, aRank *int) (diffItem, bool) {
	cat := a.Category
	if cat == "" {
		cat = b.Category
	}
	it := diffItem{ID: id, Category: cat}
	changed := false

	bState, aState := outcomeStateLabel(b), outcomeStateLabel(a)
	if bState != aState {
		it.State = &stringPair{Before: bState, After: aState}
		changed = true
	}
	if !equalIntPtr(b.Score, a.Score) {
		it.Score = &intPair{Before: b.Score, After: a.Score}
		changed = true
	}
	if b.Recommendation != a.Recommendation {
		it.Recommendation = &stringPair{Before: b.Recommendation, After: a.Recommendation}
		changed = true
	}
	if !equalBoolPtr(b.SplitFlag, a.SplitFlag) {
		it.SplitFlag = &boolPair{Before: b.SplitFlag, After: a.SplitFlag}
		changed = true
	}
	if !equalIntPtr(bRank, aRank) {
		it.Rank = &intPair{Before: bRank, After: aRank}
		changed = true
	}

	bGates := map[string]string{}
	var gateOrder []string
	seenGate := map[string]bool{}
	for _, g := range b.Gates {
		bGates[g.ID] = gateVerdictWord(g.Passed)
		seenGate[g.ID] = true
		gateOrder = append(gateOrder, g.ID)
	}
	aGates := map[string]string{}
	for _, g := range a.Gates {
		aGates[g.ID] = gateVerdictWord(g.Passed)
		if !seenGate[g.ID] {
			seenGate[g.ID] = true
			gateOrder = append(gateOrder, g.ID)
		}
	}
	for _, gid := range gateOrder {
		bv, av := dashEmpty(bGates[gid]), dashEmpty(aGates[gid])
		if bv != av {
			it.Gates = append(it.Gates, namedShift{ID: gid, Before: bv, After: av})
			changed = true
		}
	}

	bFactors := map[string]string{}
	var factorOrder []string
	seenFactor := map[string]bool{}
	for _, f := range b.Factors {
		bFactors[f.ID] = fmt.Sprintf("%d", f.Rating)
		seenFactor[f.ID] = true
		factorOrder = append(factorOrder, f.ID)
	}
	aFactors := map[string]string{}
	for _, f := range a.Factors {
		aFactors[f.ID] = fmt.Sprintf("%d", f.Rating)
		if !seenFactor[f.ID] {
			seenFactor[f.ID] = true
			factorOrder = append(factorOrder, f.ID)
		}
	}
	for _, fid := range factorOrder {
		bv, av := dashEmpty(bFactors[fid]), dashEmpty(aFactors[fid])
		if bv != av {
			it.Factors = append(it.Factors, namedShift{ID: fid, Before: bv, After: av})
			changed = true
		}
	}

	return it, changed
}

func gateVerdictWord(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func fmtIntPtr(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

func fmtBoolPtr(v *bool) string {
	if v == nil {
		return "-"
	}
	if *v {
		return "true"
	}
	return "false"
}

func dashEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
