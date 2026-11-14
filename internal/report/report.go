// Package report prints an outcome for a person to read.
package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/ClearedStack/assay/internal/contract"
)

// Text writes an outcome as plain text: each gate, each factor rating, the
// total, the recommendation and the tie-breakers. The layout is fixed, so
// the text is as repeatable as the JSON.
func Text(w io.Writer, o *contract.Outcome) error {
	var b strings.Builder

	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-10s%s\n", label+":", value)
		}
	}
	line("Item", o.ItemID)
	line("Category", o.Category)
	if o.Rules != nil {
		line("Rules", fmt.Sprintf("rubric v%d, profile %s v%d", o.Rules.Rubric, o.Rules.Profile, o.Rules.ProfileVersion))
	}
	if o.State != "" {
		line("State", o.State)
	} else {
		line("State", "(none: the record could not be read)")
	}

	if o.Error != nil {
		b.WriteString("\nError\n")
		fmt.Fprintf(&b, "  %-13s%s\n", "Code:", o.Error.Code)
		fmt.Fprintf(&b, "  %-13s%s\n", "Reason:", o.Error.Reason)
		if o.Error.Where != "" {
			fmt.Fprintf(&b, "  %-13s%s\n", "Where:", o.Error.Where)
		}
		fmt.Fprintf(&b, "  %-13s%s\n", "Fingerprint:", o.Error.Fingerprint)
	}

	if len(o.Gates) > 0 {
		b.WriteString("\nKnockout gates\n")
		for _, g := range o.Gates {
			verdict := "PASS"
			if !g.Passed {
				verdict = "FAIL"
			}
			fmt.Fprintf(&b, "  %s  %-24s%s\n", verdict, g.Name, g.Reason)
		}
	}

	if len(o.Factors) > 0 {
		b.WriteString("\nRubric\n")
		for _, f := range o.Factors {
			fmt.Fprintf(&b, "  %-30s%d x %d = %3d\n", f.Name, f.Rating, f.Weight, f.Points)
		}
		if o.Score != nil {
			fmt.Fprintf(&b, "  %-30s        %3d / 100\n", "Total", *o.Score)
		}
	}

	if o.Recommendation != "" {
		fmt.Fprintf(&b, "\nRecommendation: %s\n", o.Recommendation)
	}
	if o.SplitFlag != nil {
		answer := "no"
		if *o.SplitFlag {
			answer = "yes"
		}
		fmt.Fprintf(&b, "Split/merge review: %s\n", answer)
	}

	if len(o.TieBreakers) > 0 {
		b.WriteString("\nTie-breakers (higher sorts first)\n")
		for i, t := range o.TieBreakers {
			fmt.Fprintf(&b, "  %d. %-24s%d\n", i+1, t.Name, t.Value)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}
