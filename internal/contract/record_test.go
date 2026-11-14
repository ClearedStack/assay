package contract

import (
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/errs"
)

// good is a lease reply as Muster sends it, with one tiered fact of each kind.
const goodFacts = `{"a":{"value":true,"tier":"Seen"},"b":{"value":-12,"tier":"Stated"},"c":{"value":"token","tier":"Seen"},"d":{"value":["x","y"],"tier":"Seen"},"e":{"value":[],"tier":"Stated"},"f":{"value":null,"tier":"Unknown"}}`

const good = `{"lease":"PA-0007.3","kind":"item","id":"PA-0007","state":"observed","lane":"normal","attempt":1,` + "\n" +
	`"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing",` + "\n" +
	`"facts":` + goodFacts + `},"trawl":{"source":"made-up source"}}}`

func TestParseLeaseReadsEveryKindOfFact(t *testing.T) {
	rec, perr := ParseLease([]byte(good))
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if rec.Lease != "PA-0007.3" || rec.ItemID != "PA-0007" || rec.Category != "pricing" || len(rec.Facts) != 6 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if v := rec.Facts["a"]; v.Kind != KindBool || v.Tier != TierSeen || !v.Bool {
		t.Errorf("a: %+v", v)
	}
	if v := rec.Facts["b"]; v.Kind != KindInt || v.Tier != TierStated || v.Int != -12 {
		t.Errorf("b: %+v", v)
	}
	if v := rec.Facts["c"]; v.Kind != KindString || v.Tier != TierSeen || v.Str != "token" {
		t.Errorf("c: %+v", v)
	}
	if v := rec.Facts["d"]; v.Kind != KindList || v.Tier != TierSeen || len(v.List) != 2 || v.List[1] != "y" {
		t.Errorf("d: %+v", v)
	}
	if v := rec.Facts["e"]; v.Kind != KindList || v.Tier != TierStated || len(v.List) != 0 {
		t.Errorf("e: %+v", v)
	}
	if v := rec.Facts["f"]; v.Kind != KindUnknown || v.Tier != TierUnknown {
		t.Errorf("f: %+v", v)
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
		"what was found":           with(`"results"`, `"found":{"source":"web-sweep","title":"A made-up title","address":"https://example.invalid/x"},"results"`),
		"a lease with a colon":     with(`"PA-0007.3"`, `"release:PA-0007.3"`),
		"sextant provenance":       with(`"category":"pricing"`, `"category":"pricing","sub_tool":"far-rates","observed_on":"2026-01-15"`),
		"leap-year observed_on":    with(`"category":"pricing"`, `"category":"pricing","observed_on":"2024-02-29"`),
		"century leap observed_on": with(`"category":"pricing"`, `"category":"pricing","observed_on":"2000-02-29"`),
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
		{"invalid utf-8", with("token", "to\xffken"), errs.Parse, errs.MalformedJSON, "record", ""},
		{"two values", good + good, errs.Parse, errs.MalformedJSON, "record", ""},
		{"array", `[]`, errs.Parse, errs.NotAnObject, "record", ""},
		{"string", `"observed"`, errs.Parse, errs.NotAnObject, "record", ""},
		{"duplicate envelope key", with(`"state":"observed"`, `"state":"observed","state":"observed"`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"duplicate fact", with(`"a":{"value":true,"tier":"Seen"}`, `"a":{"value":true,"tier":"Seen"},"a":{"value":false,"tier":"Seen"}`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"duplicate key in another worker's result", with(`{"source":"made-up source"}`, `{"source":"x","source":"y"}`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"nested beyond reason", with(`{"source":"made-up source"}`, strings.Repeat("[", 40)+strings.Repeat("]", 40)), errs.Parse, errs.InvalidType, "record", ""},

		{"no lease", with(`"lease":"PA-0007.3",`, ``), errs.Parse, errs.MissingField, "lease", ""},
		{"numeric lease", with(`"lease":"PA-0007.3"`, `"lease":7`), errs.Parse, errs.InvalidType, "lease", ""},
		{"lease with a slash", with(`"PA-0007.3"`, `"PA-0007/3"`), errs.Parse, errs.InvalidValue, "lease", ""},
		{"empty lease", with(`"PA-0007.3"`, `""`), errs.Parse, errs.InvalidValue, "lease", ""},

		{"found as text", with(`"results"`, `"found":"a page","results"`), errs.Parse, errs.InvalidType, "found", "PA-0007.3"},
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
		{"numeric sub_tool", with(`"category":"pricing"`, `"category":"pricing","sub_tool":1`), errs.Parse, errs.InvalidType, "sub_tool", "PA-0007.3"},
		{"bad sub_tool slug", with(`"category":"pricing"`, `"category":"pricing","sub_tool":"Bad_Slug"`), errs.Parse, errs.InvalidValue, "sub_tool", "PA-0007.3"},
		{"numeric observed_on", with(`"category":"pricing"`, `"category":"pricing","observed_on":20260115`), errs.Parse, errs.InvalidType, "observed_on", "PA-0007.3"},
		{"non-leap observed_on", with(`"category":"pricing"`, `"category":"pricing","observed_on":"2026-02-29"`), errs.Parse, errs.InvalidValue, "observed_on", "PA-0007.3"},
		{"century non-leap observed_on", with(`"category":"pricing"`, `"category":"pricing","observed_on":"1900-02-29"`), errs.Parse, errs.InvalidValue, "observed_on", "PA-0007.3"},
		{"category but no facts", with(`,`+"\n"+`"facts":`+goodFacts, ``), errs.Parse, errs.MissingField, "facts", "PA-0007.3"},
		{"facts as a list", with(goodFacts, `[]`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"bare scalar fact", with(`{"value":true,"tier":"Seen"}`, `true`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"bare null fact", with(`{"value":true,"tier":"Seen"}`, `null`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"fact missing tier", with(`{"value":true,"tier":"Seen"}`, `{"value":true}`), errs.Parse, errs.MissingField, "facts", "PA-0007.3"},
		{"fact missing value", with(`{"value":true,"tier":"Seen"}`, `{"tier":"Seen"}`), errs.Parse, errs.MissingField, "facts", "PA-0007.3"},
		{"fact with extra key", with(`{"value":true,"tier":"Seen"}`, `{"value":true,"tier":"Seen","note":"x"}`), errs.Parse, errs.InvalidValue, "facts", "PA-0007.3"},
		{"numeric tier", with(`{"value":true,"tier":"Seen"}`, `{"value":true,"tier":1}`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"lower-case tier", with(`{"value":true,"tier":"Seen"}`, `{"value":true,"tier":"seen"}`), errs.Parse, errs.InvalidValue, "facts", "PA-0007.3"},
		{"Unknown tier with value", with(`{"value":null,"tier":"Unknown"}`, `{"value":true,"tier":"Unknown"}`), errs.Parse, errs.InvalidValue, "facts", "PA-0007.3"},
		{"Seen tier with null", with(`{"value":true,"tier":"Seen"}`, `{"value":null,"tier":"Seen"}`), errs.Parse, errs.InvalidValue, "facts", "PA-0007.3"},
		{"Stated tier with null", with(`{"value":-12,"tier":"Stated"}`, `{"value":null,"tier":"Stated"}`), errs.Parse, errs.InvalidValue, "facts", "PA-0007.3"},
		{"fraction", with(`-12`, `1.5`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"exponent", with(`-12`, `1e3`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"too big for 64 bits", with(`-12`, `9223372036854775808`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"object inside fact value", with(`{"value":true,"tier":"Seen"}`, `{"value":{"x":1},"tier":"Seen"}`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"number in a list", with(`"y"`, `3`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"list in a list", with(`"y"`, `["y"]`), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},

		{"unknown envelope field", with(`"state"`, `"priority":"high","state"`), errs.NeedsSubtool, errs.UnknownField, "record", "PA-0007.3"},
		{"a proposal body beside an item", with(`"state"`, `"proposal":{},"state"`), errs.NeedsSubtool, errs.UnknownField, "record", "PA-0007.3"},
		{"unknown field in the observation", with(`"category":"pricing"`, `"category":"pricing","tier":"Seen"`), errs.NeedsSubtool, errs.UnknownField, "sextant", "PA-0007.3"},
		{"observation with no category", with(`"category":"pricing",`+"\n", ``), errs.NeedsSubtool, errs.UnknownCategory, "category", "PA-0007.3"},
		{"observation in another shape entirely", with(`{"category":"pricing",`+"\n"+`"facts":`+goodFacts+`}`, `{"sub_tool":"made-up","core_job":{"value":"x","tier":"Seen"}}`), errs.NeedsSubtool, errs.UnknownCategory, "category", "PA-0007.3"},
		{"broken beats unfamiliar", with(`"state":"observed"`, `"priority":"high","state":"done"`), errs.Parse, errs.InvalidValue, "state", "PA-0007.3"},
		{"broken facts beat a missing category", strings.Replace(with(`"category":"pricing",`+"\n", ``), `-12`, `1.5`, 1), errs.Parse, errs.InvalidType, "facts", "PA-0007.3"},
		{"broken observed_on beats a missing category", with(`"category":"pricing"`, `"observed_on":"2026-02-29"`), errs.Parse, errs.InvalidValue, "observed_on", "PA-0007.3"},
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
	big := strings.Replace(good, "token", strings.Repeat("x", MaxLeaseBytes), 1)
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
	score := 86
	flag := false
	o := &Outcome{
		Contract: Version, Worker: Worker, ItemID: "PA-0007", State: StateScored,
		Category: "pricing", Profile: "pricing", Score: &score, Recommendation: "build",
		SplitFlag: &flag,
		Rules: &RulesRef{
			Rubric: 1, Profile: "pricing", ProfileVersion: 1,
			Gates:   RuleRows{{ID: "public_source", Rule: 1}},
			Factors: RuleRows{{ID: "need", Rule: 2}},
		},
		Gates:   GateVerdicts{{ID: "public_source", Name: "Public source", Passed: true, Rule: 1, Reason: "it's <&> fine"}},
		Factors: FactorRatings{{ID: "need", Name: "Need", Rating: 4, Weight: 4, Points: 16, Rule: 2}},
	}
	first := Encode(o)
	if string(first) != string(Encode(o)) {
		t.Fatal("encoding the same outcome twice gave different bytes")
	}
	if !strings.Contains(string(first), `"it's <&> fine"`) || !strings.HasSuffix(string(first), "}\n") || !strings.Contains(string(first), `"id": "PA-0007"`) {
		t.Errorf("unexpected encoding:\n%s", first)
	}
}

func TestCheckEvaluationMatchesLogbook(t *testing.T) {
	const goodScored = `{
		"profile": "sample-profile",
		"gates": {"sample_gate_a": {"passed": true}, "sample_gate_b": {"passed": true, "reason": "Clear."}},
		"factors": {"sample_factor_a": {"rating": 5, "weight": 4}, "sample_factor_b": {"rating": 2, "weight": 3}},
		"score": 26,
		"recommendation": "reject",
		"split_flag": true
	}`
	const goodScreenedOut = `{
		"profile": "sample-profile",
		"gates": {"sample_gate_a": {"passed": true}, "sample_gate_b": {"passed": false, "reason": "Sample reason."}}
	}`
	if perr := CheckEvaluation([]byte(goodScored)); perr != nil {
		t.Fatalf("valid scored evaluation refused: %v", perr)
	}
	if perr := CheckEvaluation([]byte(goodScreenedOut)); perr != nil {
		t.Fatalf("valid screened_out evaluation refused: %v", perr)
	}

	parseRefusals := map[string]string{
		"wrong score":                 strings.Replace(goodScored, `"score": 26`, `"score": 27`, 1),
		"missing score":               strings.Replace(goodScored, `"score": 26,`, ``, 1),
		"missing recommendation":      strings.Replace(goodScored, `"recommendation": "reject",`, ``, 1),
		"rating above 5":              strings.Replace(goodScored, `"rating": 5`, `"rating": 6`, 1),
		"rating 0":                    strings.Replace(goodScored, `"rating": 5`, `"rating": 0`, 1),
		"fractional rating":           strings.Replace(goodScored, `"rating": 5`, `"rating": 2.5`, 1),
		"weight 0":                    strings.Replace(goodScored, `"weight": 4`, `"weight": 0`, 1),
		"extra factor field":          strings.Replace(goodScored, `"weight": 4}`, `"weight": 4, "rule": 1}`, 1),
		"bad factor slug":             strings.Replace(goodScored, `"sample_factor_a"`, `"Sample Factor"`, 1),
		"factors as array":            strings.Replace(goodScored, `{"sample_factor_a": {"rating": 5, "weight": 4}, "sample_factor_b": {"rating": 2, "weight": 3}}`, `[]`, 1),
		"empty factors when passed":   strings.Replace(goodScored, `{"sample_factor_a": {"rating": 5, "weight": 4}, "sample_factor_b": {"rating": 2, "weight": 3}}`, `{}`, 1),
		"gates as array":              `{"profile":"sample-profile","gates":[{"id":"g","pass":true}]}`,
		"empty gates":                 `{"profile":"sample-profile","gates":{}}`,
		"extra gate field":            strings.Replace(goodScreenedOut, `"passed": true}`, `"passed": true, "rule": 1}`, 1),
		"failed gate without reason":  `{"profile":"sample-profile","gates":{"sample_gate_a":{"passed":false}}}`,
		"failed gate blank reason":    `{"profile":"sample-profile","gates":{"sample_gate_a":{"passed":false,"reason":"  "}}}`,
		"failed gate with factors":    strings.TrimSuffix(goodScreenedOut, "}") + `,"factors":{"a":{"rating":5,"weight":4}}}`,
		"failed gate with score":      strings.TrimSuffix(goodScreenedOut, "}") + `,"score":20}`,
		"failed gate with rec":        strings.TrimSuffix(goodScreenedOut, "}") + `,"recommendation":"reject"}`,
		"failed gate with split_flag": strings.TrimSuffix(goodScreenedOut, "}") + `,"split_flag":true}`,
		"missing profile":             strings.Replace(goodScreenedOut, `"profile": "sample-profile",`, ``, 1),
		"bad profile slug":            strings.Replace(goodScreenedOut, `"sample-profile"`, `"Sample Profile"`, 1),
	}
	for name, bad := range parseRefusals {
		if perr := CheckEvaluation([]byte(bad)); perr == nil || perr.Code != errs.Parse {
			t.Errorf("%s: want E_PARSE, got %v", name, perr)
		}
	}

	unknownRecs := []string{
		strings.Replace(goodScored, `"reject"`, `"Build"`, 1),
		strings.Replace(goodScored, `"reject"`, `"merge"`, 1),
	}
	for _, rec := range unknownRecs {
		if perr := CheckEvaluation([]byte(rec)); perr == nil || perr.Code != errs.NeedsSubtool {
			t.Errorf("unknown recommendation: want E_NEEDS_SUBTOOL, got %v", perr)
		}
	}
}

func TestStatesAreSpelledAsMusterSpellsThem(t *testing.T) {
	if StateScored != "scored" || StateScreenedOut != "screened_out" || StateNeedsSubtool != "needs_subtool" || StateObserved != "observed" {
		t.Error("state names are fixed by Muster's contract")
	}
}
