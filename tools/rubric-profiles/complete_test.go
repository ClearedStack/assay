package rubricprofiles

import (
	"os"
	"testing"
)

// TestTablesAnswerEveryCandidateThatClearsTheGates throws a large, fixed set
// of made-up candidates at each profile. Each fact is drawn from the values
// that matter: every declared value of an enum, and for a number the
// thresholds the rules mention, one either side of each, and the ends of
// the declared range.
//
// Every Seen candidate must get a gate verdict on all five gates, and every
// candidate that clears the gates must get a rating for every factor. A hole
// in a table would otherwise only show up when a real item fell into it.
func TestTablesAnswerEveryCandidateThatClearsTheGates(t *testing.T) {
	set, err := Load(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range CheckCompleteness(set, DefaultCompletenessSamples) {
		t.Error(problem)
	}
}
