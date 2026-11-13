package errs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestErrorCarriesOnlyFixedWords(t *testing.T) {
	secret := "db01.internal.example:5432 password=hunter2"
	e := New(NeedsSubtool, UnknownFact, "facts", "pricing", secret)

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(data), e.Error()} {
		for _, leak := range []string{"db01", "internal", "5432", "hunter2", "password"} {
			if strings.Contains(text, leak) {
				t.Errorf("error text %q leaks %q", text, leak)
			}
		}
	}
	if e.Error() != "E_NEEDS_SUBTOOL: unknown_fact (facts)" {
		t.Errorf("unexpected text %q", e.Error())
	}
}

func TestFingerprintIsStableAndSpecific(t *testing.T) {
	a := New(NeedsSubtool, UnknownValue, "source_kind", "pricing", "treaty")
	b := New(NeedsSubtool, UnknownValue, "source_kind", "pricing", "treaty")
	c := New(NeedsSubtool, UnknownValue, "source_kind", "pricing", "charter")
	d := New(NeedsSubtool, UnknownValue, "source_kind", "cyber", "treaty")
	if a.Fingerprint != b.Fingerprint {
		t.Error("the same failure must give the same fingerprint")
	}
	if a.Fingerprint == c.Fingerprint || a.Fingerprint == d.Fingerprint {
		t.Error("different failures must give different fingerprints")
	}
	// Pinned: a change to how fingerprints are made would silently break
	// deduplication of open reports, so it must fail a test.
	if a.Fingerprint != "ee7ec3c33e5b0b57" {
		t.Errorf("fingerprint changed: %s", a.Fingerprint)
	}
	// The parts are separated, so ("ab","c") and ("a","bc") differ.
	if New(Parse, InvalidType, "ab", "c").Fingerprint == New(Parse, InvalidType, "a", "bc").Fingerprint {
		t.Error("fingerprint parts run together")
	}
}

func TestThereAreExactlyThreeCodes(t *testing.T) {
	if Timeout != "E_TIMEOUT" || Parse != "E_PARSE" || NeedsSubtool != "E_NEEDS_SUBTOOL" {
		t.Error("the public codes are fixed by the specification")
	}
}
