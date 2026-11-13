package contract

import (
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/errs"
)

// good is a lease reply as Muster sends it, with one fact of each kind.
const good = `{"lease":"PA-0007.3","kind":"item","id":"PA-0007","state":"observed","lane":"normal","attempt":1,
"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing",
"facts":{"a":true,"b":-12,"c":"text","d":["x","y"],"e":[]}},"trawl":{"source":"made-up source"}}}`

const goodFacts = `{"a":true,"b":-12,"c":"text","d":["x","y"],"e":[]}`

func TestParseLeaseReadsEveryKindOfFact(t *testing.T) {
	rec, perr := ParseLease([]byte(good))
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if rec.Lease != "PA-0007.3" || rec.ItemID != "PA-0007" || rec.Category != "pricing" || len(rec.Facts) != 5 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if v := rec.Facts["a"]; v.Kind != KindBool || !v.Bool {
		t.Errorf("a: %+v", v)
	}
	if v := rec.Facts["b"]; v.Kind != KindInt || v.Int != -12 {
		t.Errorf("b: %+v", v)
	}
	if v := rec.Facts["c"]; v.Kind != KindString || v.Str != "text" {
		t.Errorf("c: %+v", v)
	}
	if v := rec.Facts["d"]; v.Kind != KindList || len(v.List) != 2 || v.List[1] != "y" {
		t.Errorf("d: %+v", v)
	}
	if v := rec.Facts["e"]; v.Kind != KindList || len(v.List) != 0 {
		t.Errorf("e: %+v", v)
	}
}

func TestParseLeaseAcceptsWhatMusterMaySend(t *testing.T) {
	with := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	cases := map[string]string{
		"urgent lane":              with(`"normal"`, `"urgent"`),
		"backfill lane":            with(`"normal"`, `"backfill"`),
		"a later attempt":          with(`"attempt":1`, `"attempt":7`),
		"five-digit item":          with(`"id":"PA-0007"`, `"id":"PA-10000"`),
		"nine-digit item":          with(`"id":"PA-0007"`, `"id":"PA-999999999"`),
		"fractional seconds":       with(`09:01:00Z`, `09:01:00.250Z`),
		"an offset":                with(`09:01:00Z`, `09:01:00+00:00`),
		"results of other workers": with(`"trawl":{"source":"made-up source"}`, `"trawl":{"a":{"b":{"c":[[1,2.5,null,{"d":"deep"}]]}}},"countersign":{"decision":"override"}`),
		"no results but Sextant's": with(`,"trawl":{"source":"made-up source"}`, ``),
	}
	for name, input := range cases {
		if _, perr := ParseLease([]byte(input)); perr != nil {
			t.Errorf("%s: refused with %v", name, perr)
		}
	}
}

func TestParseLeaseRefusals(t *testing.T) {
	with := func(old, new string) string {
		if !strings.Contains(good, old) {
			t.Fatalf("the text %q is not in the good reply; the test is out of date", old)
		}
		return strings.Replace(good, old, new, 1)
	}
	cases := []struct {
		name   string
		input  string
		code   errs.Code
		reason errs.Reason
		where  string
		lease  string // what must still be known, so the failure can be reported
	}{
		{"empty", "", errs.Parse, errs.MalformedJSON, "record", ""},
		{"not json", "category: pricing", errs.Parse, errs.MalformedJSON, "record", ""},
		{"invalid utf-8", with("text", "te\xffxt"), errs.Parse, errs.MalformedJSON, "record", ""},
		{"two values", good + good, errs.Parse, errs.MalformedJSON, "record", ""},
		{"array", `[]`, errs.Parse, errs.NotAnObject, "record", ""},
		{"string", `"observed"`, errs.Parse, errs.NotAnObject, "record", ""},
		{"duplicate envelope key", with(`"state":"observed"`, `"state":"observed","state":"observed"`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"duplicate fact", with(`"a":true`, `"a":true,"a":false`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"duplicate key in another worker's result", with(`{"source":"made-up source"}`, `{"source":"x","source":"y"}`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"nested beyond reason", with(`{"source":"made-up source"}`, strings.Repeat("[", 40)+strings.Repeat("]", 40)), errs.Parse, errs.InvalidType, "record", ""},

		{"no lease", with(`"lease":"PA-0007.3",`, ``), errs.Parse, errs.MissingField, "lease", ""},
		{"numeric lease", with(`"lease":"PA-0007.3"`, `"lease":7`), errs.Parse, errs.InvalidType, "lease", ""},
		{"lease with a slash", with(`"PA-0007.3"`, `"PA-0007/3"`), errs.Parse, errs.InvalidValue, "lease", ""},
		{"empty lease", with(`"PA-0007.3"`, `""`), errs.Parse, errs.InvalidValue, "lease", ""},

		{"no id", with(`"id":"PA-0007",`, ``), errs.Parse, errs.MissingField, "id", "PA-0007.3"},
		{"numeric id", with(`"id":"PA-0007"`, `"id":7`), errs.Parse, errs.InvalidType, "id", "PA-0007.3"},
		{"three-digit id", with(`"id":"PA-0007"`, `"id":"PA-007"`), errs.Parse, errs.InvalidValue, "id", "PA-0007.3"},
		{"padded five-digit id", with(`"id":"PA-0007"`, `"id":"PA-00007"`), errs.Parse, errs.InvalidValue, "id", "PA-0007.3"},
		{"ten-digit id", with(`"id":"PA-0007"`, `"id":"PA-1234567890"`), errs.Parse, errs.InvalidValue, "id", "PA-0007.3"},
		{"lower-case id", with(`"id":"PA-0007"`, `"id":"pa-0007"`), errs.Parse, errs.InvalidValue, "id", "PA-0007.3"},
		{"proposal id", with(`"id":"PA-0007"`, `"id":"EP-0007"`), errs.Parse, errs.InvalidValue, "id", "PA-0007.3"},

		{"no kind", with(`"kind":"item",`, ``), errs.Parse, errs.MissingField, "kind", "PA-0007.3"},
		{"a proposal", with(`"item"`, `"proposal"`), errs.Parse, errs.InvalidValue, "kind", "PA-0007.3"},
		{"wrong state", with(`"observed"`, `"scored"`), errs.Parse, errs.InvalidValue, "state", "PA-0007.3"},
		{"old spelling of a state", with(`"observed"`, `"screened-out"`), errs.Parse, errs.InvalidValue, "state", "PA-0007.3"},
		{"unknown lane", with(`"normal"`, `"express"`), errs.Parse, errs.InvalidValue, "lane", "PA-0007.3"},
		{"no attempt", with(`"attempt":1,`, ``), errs.Parse, errs.MissingField, "attempt", "PA-0007.3"},
		{"attempt zero", with(`"attempt":1`, `"attempt":0`), errs.Parse, errs.InvalidValue, "attempt", "PA-0007.3"},
		{"fractional attempt", with(`"attempt":1`, `"attempt":1.5`), errs.Parse, errs.InvalidType, "attempt", "PA-0007.3"},
		{"no expires", with("\"expires\":\"2026-01-05T09:01:00Z\",", ``), errs.Parse, errs.MissingField, "expires", "PA-0007.3"},
		{"expires not a time", with(`2026-01-05T09:01:00Z`, `soon`), errs.Parse, errs.InvalidValue, "expires", "PA-0007.3"},
		{"expires with no zone", with(`09:01:00Z`, `09:01:00`), errs.Parse, errs.InvalidValue, "expires", "PA-0007.3"},

		{"results as a list", with(`{"sextant":{"category":"pricing",`+"\n"+`"facts":`+goodFacts+`},"trawl":{"source":"made-up source"}}`, `[]`), errs.Parse, errs.InvalidType, "results", "PA-0007.3"},
		{"no observation", with(`"sextant":{"category":"pricing",`+"\n"+`"facts":`+goodFacts+`},`, ``), errs.Parse, errs.MissingField, "sextant", "PA-0007.3"},
		{"null category", with(`"pricing"`, `null`), errs.Parse, errs.InvalidType, "category", "PA-0007.3"},
		{"upper-case category", with(`"pricing"`, `"Pricing"`), errs.Parse, errs.InvalidValue, "category", "PA-0007.3"},
		{"category but no facts", with(`,`+"\n"+`"facts":`+goodFacts, ``), errs.Parse, errs.MissingField, "facts", "PA-0007.3"},
		{"facts as a list", with(goodFacts, `[]`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"fraction", with(`-12`, `1.5`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"exponent", with(`-12`, `1e3`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"too big for 64 bits", with(`-12`, `9223372036854775808`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"null fact", with(`true`, `null`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"object fact", with(`true`, `{"x":1}`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"number in a list", with(`"y"`, `3`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"list in a list", with(`"y"`, `["y"]`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},

		{"unknown envelope field", with(`"state"`, `"priority":"high","state"`), errs.NeedsSubtool, errs.UnknownField, "record", "PA-0007.3"},
		{"a proposal body beside an item", with(`"state"`, `"proposal":{},"state"`), errs.NeedsSubtool, errs.UnknownField, "record", "PA-0007.3"},
		{"unknown field in the observation", with(`"category":"pricing"`, `"category":"pricing","tier":"Seen"`), errs.NeedsSubtool, errs.UnknownField, "sextant", "PA-0007.3"},
		{"observation with no category", with(`"category":"pricing",`+"\n", ``), errs.NeedsSubtool, errs.UnknownCategory, "category", "PA-0007.3"},
		{"observation in another shape entirely", with(`{"category":"pricing",`+"\n"+`"facts":`+goodFacts+`}`, `{"sub_tool":"made-up","core_job":{"value":"x","tier":"Seen"}}`), errs.NeedsSubtool, errs.UnknownCategory, "category", "PA-0007.3"},
		{"broken beats unfamiliar", with(`"state":"observed"`, `"priority":"high","state":"done"`), errs.Parse, errs.InvalidValue, "state", "PA-0007.3"},
		{"broken facts beat a missing category", strings.Replace(with(`"category":"pricing",`+"\n", ``), `-12`, `1.5`, 1), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, perr := ParseLease([]byte(tc.input))
			if perr == nil {
				t.Fatal("expected an error")
			}
			if perr.Code != tc.code || perr.Reason != tc.reason || perr.Where != tc.where {
				t.Errorf("got %s %s (%s), want %s %s (%s)", perr.Code, perr.Reason, perr.Where, tc.code, tc.reason, tc.where)
			}
			if rec == nil || rec.Lease != tc.lease {
				t.Errorf("lease: got %q, want %q", rec.Lease, tc.lease)
			}
			if id, ok := LeaseID([]byte(tc.input)); (tc.lease != "") != ok || id != tc.lease {
				t.Errorf("LeaseID: got %q, %v; want %q", id, ok, tc.lease)
			}
		})
	}
}

func TestParseLeaseRefusesAnOversizeReply(t *testing.T) {
	big := strings.Replace(good, "text", strings.Repeat("x", MaxLeaseBytes), 1)
	_, perr := ParseLease([]byte(big))
	if perr == nil || perr.Code != errs.Parse || perr.Reason != errs.TooLarge {
		t.Fatalf("got %v", perr)
	}
	if _, ok := LeaseID([]byte(big)); ok {
		t.Error("an oversize reply must not be read at all")
	}
}

func TestUnknownFieldFingerprintDependsOnTheFieldButDoesNotShowIt(t *testing.T) {
	a := strings.Replace(good, `"state"`, `"zeta":1,"alpha":1,"state"`, 1)
	b := strings.Replace(good, `"state"`, `"alpha":1,"zeta":1,"state"`, 1)
	c := strings.Replace(good, `"state"`, `"beta":1,"state"`, 1)
	_, ea := ParseLease([]byte(a))
	_, eb := ParseLease([]byte(b))
	_, ec := ParseLease([]byte(c))
	if ea.Fingerprint != eb.Fingerprint {
		t.Error("the order of fields in the input must not change the error")
	}
	if ea.Fingerprint == ec.Fingerprint {
		t.Error("a different unknown field should give a different fingerprint")
	}
}

func TestEncodeIsStable(t *testing.T) {
	total := 86
	flag := false
	o := &Outcome{Contract: Version, Worker: Worker, ItemID: "PA-0007", State: StateScored, Total: &total,
		SplitMergeReview: &flag, Gates: []GateResult{{ID: "g", Name: "G <&>", Pass: true, Rule: 1, Reason: "it's fine"}}}
	first := Encode(o)
	if string(first) != string(Encode(o)) {
		t.Fatal("encoding the same outcome twice gave different bytes")
	}
	if !strings.Contains(string(first), `"G <&>"`) || !strings.HasSuffix(string(first), "}\n") || !strings.Contains(string(first), `"id": "PA-0007"`) {
		t.Errorf("unexpected encoding:\n%s", first)
	}
}

func TestStatesAreSpelledAsMusterSpellsThem(t *testing.T) {
	if StateScored != "scored" || StateScreenedOut != "screened_out" || StateNeedsSubtool != "needs_subtool" || StateObserved != "observed" {
		t.Error("state names are fixed by Muster's contract")
	}
}
