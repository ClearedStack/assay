package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ClearedStack/assay/internal/errs"
)

func isLowerHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func FuzzParse(f *testing.F) {
	seedGlobs := []string{
		"../../testdata/contract/*.json",
		"../../tools/rubric-profiles/fixtures/*.input.json",
		"../../tools/rubric-profiles/pricing/fixtures/*.input.json",
	}
	for _, pattern := range seedGlobs {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			if data, err := os.ReadFile(p); err == nil {
				f.Add(data)
			}
		}
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"lease":"PA-0001.1","lease":"PA-0001.1"}`))
	f.Add([]byte(`{"lease":"PA-0001.1","kind":"item","id":"PA-0001","state":"observed","lane":"normal","attempt":1,"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing","facts":{"f":{"value":null,"tier":"Seen"}}}}}`))
	f.Add([]byte(`{"lease":"PA-0001.1","kind":"item","id":"PA-0001","state":"observed","lane":"normal","attempt":1,"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing","facts":{"f":{"value":false,"tier":"Designed"}}}}}`))
	f.Add([]byte(`{"lease":"PA-0001.1","kind":"item","id":"PA-0001","state":"observed","lane":"normal","attempt":1,"expires":"2026-01-05T09:01:00Z","results":{"sextant":{"category":"pricing","facts":{"f":{"value":null,"tier":"Designed"}}}}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		rec1, err1 := ParseLease(data)
		rec2, err2 := ParseLease(data)

		if !reflect.DeepEqual(rec1, rec2) {
			t.Fatalf("ParseLease record is not deterministic")
		}
		if !reflect.DeepEqual(err1, err2) {
			t.Fatalf("ParseLease error is not deterministic: %v vs %v", err1, err2)
		}

		id1, ok1 := LeaseID(data)
		id2, ok2 := LeaseID(data)
		if id1 != id2 || ok1 != ok2 {
			t.Fatalf("LeaseID is not deterministic")
		}

		if len(data) > MaxLeaseBytes {
			if err1 == nil || err1.Code != errs.Parse || err1.Reason != errs.TooLarge {
				t.Fatalf("oversized lease (%d bytes) gave %v, want E_PARSE too_large", len(data), err1)
			}
		}

		if err1 != nil {
			if err1.Code != errs.Parse && err1.Code != errs.NeedsSubtool {
				t.Fatalf("unexpected error code %q", err1.Code)
			}
			if err1.Reason == "" {
				t.Fatalf("empty error reason")
			}
			if !isLowerHex16(err1.Fingerprint) {
				t.Fatalf("malformed fingerprint %q", err1.Fingerprint)
			}
		}
	})
}

func FuzzOutcomeEvaluation(f *testing.F) {
	paths, _ := filepath.Glob("../../tools/rubric-profiles/*/fixtures/*.golden.json")
	for i, p := range paths {
		if i >= 30 {
			break
		}
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`{"state":"scored","profile":"pricing","gates":{"g":{"passed":true,"reason":"ok"}},"factors":{"f":{"rating":5,"weight":20}},"score":100,"recommendation":"build","split_flag":false}`))
	f.Add([]byte(`{"state":"screened_out","profile":"pricing","gates":{"g":{"passed":false,"reason":"no"}}}`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cerr1 := CheckEvaluation(data)
		cerr2 := CheckEvaluation(data)
		if !reflect.DeepEqual(cerr1, cerr2) {
			t.Fatalf("CheckEvaluation is not deterministic: %v vs %v", cerr1, cerr2)
		}
		if cerr1 != nil && !isLowerHex16(cerr1.Fingerprint) {
			t.Fatalf("malformed CheckEvaluation fingerprint %q", cerr1.Fingerprint)
		}

		var o1, o2 Outcome
		uerr1 := json.Unmarshal(data, &o1)
		uerr2 := json.Unmarshal(data, &o2)
		if (uerr1 == nil) != (uerr2 == nil) {
			t.Fatalf("Outcome.UnmarshalJSON error determinism mismatch: %v vs %v", uerr1, uerr2)
		}
		if uerr1 == nil {
			enc1 := Encode(&o1)
			enc2 := Encode(&o2)
			if !bytes.Equal(enc1, enc2) {
				t.Fatalf("Encode(Outcome) is not deterministic")
			}
		}
	})
}
