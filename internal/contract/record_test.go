package contract

import (
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/errs"
)

const good = `{"contract_version":"v1","item_id":"cand-1","state":"observed","category":"pricing",
"facts":{"a":true,"b":-12,"c":"text","d":["x","y"],"e":[]}}`

func TestParseRecordReadsEveryKindOfFact(t *testing.T) {
	rec, perr := ParseRecord([]byte(good))
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if rec.ItemID != "cand-1" || rec.Category != "pricing" || len(rec.Facts) != 5 {
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

func TestParseRecordRefusals(t *testing.T) {
	with := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	cases := []struct {
		name   string
		input  string
		code   errs.Code
		reason errs.Reason
		where  string
		itemID string // what must still be known about the item
	}{
		{"empty", "", errs.Parse, errs.MalformedJSON, "record", ""},
		{"not json", "category: pricing", errs.Parse, errs.MalformedJSON, "record", ""},
		{"invalid utf-8", with("text", "te\xffxt"), errs.Parse, errs.MalformedJSON, "record", ""},
		{"two values", good + good, errs.Parse, errs.MalformedJSON, "record", ""},
		{"array", `[]`, errs.Parse, errs.NotAnObject, "record", ""},
		{"string", `"observed"`, errs.Parse, errs.NotAnObject, "record", ""},
		{"duplicate envelope key", with(`"state":"observed"`, `"state":"observed","state":"observed"`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"duplicate fact", with(`"a":true`, `"a":true,"a":false`), errs.Parse, errs.DuplicateKey, "record", ""},
		{"too deep", with(`["x","y"]`, `[["x"]]`), errs.Parse, errs.InvalidType, "record", ""},
		{"no item id", with(`"item_id":"cand-1",`, ``), errs.Parse, errs.MissingField, "item_id", ""},
		{"numeric item id", with(`"cand-1"`, `7`), errs.Parse, errs.InvalidType, "item_id", ""},
		{"item id with a dot", with(`"cand-1"`, `"cand.1"`), errs.Parse, errs.InvalidValue, "item_id", ""},
		{"empty item id", with(`"cand-1"`, `""`), errs.Parse, errs.InvalidValue, "item_id", ""},
		{"no contract version", with(`"contract_version":"v1",`, ``), errs.Parse, errs.MissingField, "contract_version", "cand-1"},
		{"other contract version", with(`"v1"`, `"v2"`), errs.Parse, errs.UnsupportedContract, "contract_version", "cand-1"},
		{"wrong state", with(`"observed"`, `"scored"`), errs.Parse, errs.InvalidValue, "state", "cand-1"},
		{"null category", with(`"pricing"`, `null`), errs.Parse, errs.InvalidType, "category", "cand-1"},
		{"upper-case category", with(`"pricing"`, `"Pricing"`), errs.Parse, errs.InvalidValue, "category", "cand-1"},
		{"no facts", `{"contract_version":"v1","item_id":"cand-1","state":"observed","category":"pricing"}`, errs.Parse, errs.MissingField, "facts", "cand-1"},
		{"facts as a list", with(`{"a":true,"b":-12,"c":"text","d":["x","y"],"e":[]}`, `[]`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"fraction", with(`-12`, `1.5`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"exponent", with(`-12`, `1e3`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"too big for 64 bits", with(`-12`, `9223372036854775808`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"null fact", with(`true`, `null`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"object fact", with(`true`, `{"x":1}`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"number in a list", with(`"y"`, `3`), errs.Parse, errs.InvalidType, "facts", "cand-1"},
		{"unknown envelope field", with(`"state"`, `"lane":"urgent","state"`), errs.NeedsSubtool, errs.UnknownField, "record", "cand-1"},
		{"broken beats unfamiliar", with(`"state":"observed"`, `"lane":"urgent","state":"done"`), errs.Parse, errs.InvalidValue, "state", "cand-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, perr := ParseRecord([]byte(tc.input))
			if perr == nil {
				t.Fatal("expected an error")
			}
			if perr.Code != tc.code || perr.Reason != tc.reason || perr.Where != tc.where {
				t.Errorf("got %s %s (%s), want %s %s (%s)", perr.Code, perr.Reason, perr.Where, tc.code, tc.reason, tc.where)
			}
			if rec == nil || rec.ItemID != tc.itemID {
				t.Errorf("item id: got %q, want %q", rec.ItemID, tc.itemID)
			}
		})
	}
}

func TestParseRecordRefusesAnOversizeRecord(t *testing.T) {
	big := strings.Replace(good, "text", strings.Repeat("x", MaxRecordBytes), 1)
	_, perr := ParseRecord([]byte(big))
	if perr == nil || perr.Code != errs.Parse || perr.Reason != errs.TooLarge {
		t.Fatalf("got %v", perr)
	}
}

func TestUnknownFieldFingerprintDependsOnTheFieldButDoesNotShowIt(t *testing.T) {
	a := strings.Replace(good, `"state"`, `"zeta":1,"alpha":1,"state"`, 1)
	b := strings.Replace(good, `"state"`, `"alpha":1,"zeta":1,"state"`, 1)
	c := strings.Replace(good, `"state"`, `"beta":1,"state"`, 1)
	_, ea := ParseRecord([]byte(a))
	_, eb := ParseRecord([]byte(b))
	_, ec := ParseRecord([]byte(c))
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
	o := &Outcome{ContractVersion: Version, Worker: Worker, ItemID: "cand-1", State: StateScored, Total: &total,
		SplitMergeReview: &flag, Gates: []GateResult{{ID: "g", Name: "G <&>", Pass: true, Rule: 1, Reason: "it's fine"}}}
	first := Encode(o)
	if string(first) != string(Encode(o)) {
		t.Fatal("encoding the same outcome twice gave different bytes")
	}
	if !strings.Contains(string(first), `"G <&>"`) || !strings.HasSuffix(string(first), "}\n") {
		t.Errorf("unexpected encoding:\n%s", first)
	}
}
