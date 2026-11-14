package yamlite

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func FuzzParseAll(f *testing.F) {
	seedFiles := []string{
		"../../tools/registry.yaml",
		"../../tools/rubric-profiles/rubric.yaml",
		"../../tools/rubric-profiles/pricing/match.yaml",
		"../../tools/rubric-profiles/pricing/profile.yaml",
		"../../.ko.yaml",
		"../../deploy/assay.yaml",
	}
	for _, path := range seedFiles {
		if data, err := os.ReadFile(path); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte("a: [1, 2, true]\n---\nb: {k: \"v\", n: -4}\n"))
	f.Add([]byte("tab:\n\tbad: 1\n"))
	f.Add([]byte("cr: 1\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		docs1, err1 := ParseAll(data)
		docs2, err2 := ParseAll(data)

		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("ParseAll error determinism mismatch: %v vs %v", err1, err2)
		}
		if err1 != nil {
			if err1.Error() != err2.Error() {
				t.Fatalf("ParseAll error message mismatch: %q vs %q", err1.Error(), err2.Error())
			}
		} else if !reflect.DeepEqual(docs1, docs2) {
			t.Fatalf("ParseAll AST is not deterministic")
		}

		if bytes.IndexByte(data, '\r') >= 0 && err1 == nil {
			t.Fatalf("ParseAll accepted input containing carriage return")
		}

		single, serr := Parse(data)
		if err1 != nil {
			if serr == nil {
				t.Fatalf("Parse succeeded when ParseAll failed: %v", err1)
			}
			return
		}
		if len(docs1) == 1 {
			if serr != nil || !reflect.DeepEqual(single, docs1[0]) {
				t.Fatalf("Parse disagreed with single-document ParseAll: %v", serr)
			}
		} else if serr == nil {
			t.Fatalf("Parse succeeded with %d documents", len(docs1))
		}
	})
}
