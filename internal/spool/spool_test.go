package spool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutListRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cs")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := s.List(); len(entries) != 0 {
		t.Fatalf("a new spool should be empty, got %d", len(entries))
	}

	if err := s.Put("lease-000001", []byte(`{"state":"scored"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("lease-000002", []byte(`{"state":"screened-out"}`)); err != nil {
		t.Fatal(err)
	}
	// Saving the same lease again replaces the entry; it does not add one.
	if err := s.Put("lease-000001", []byte(`{"state":"scored","total":86}`)); err != nil {
		t.Fatal(err)
	}

	entries, err := s.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d (%v)", len(entries), err)
	}
	byLease := map[string]string{}
	for _, e := range entries {
		byLease[e.LeaseID] = string(e.Outcome)
	}
	if byLease["lease-000001"] != `{"state":"scored","total":86}` || byLease["lease-000002"] != `{"state":"screened-out"}` {
		t.Errorf("unexpected entries: %v", byLease)
	}

	// A second process opening the same folder sees the same entries: this
	// is what survives a restart.
	again, _ := Open(dir)
	if entries2, _ := again.List(); len(entries2) != 2 {
		t.Errorf("after reopening: %d entries", len(entries2))
	}

	for _, e := range entries {
		if err := s.Remove(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove(entries[0]); err != nil {
		t.Errorf("removing twice should be harmless: %v", err)
	}
	if left, _ := s.List(); len(left) != 0 {
		t.Errorf("%d entries left", len(left))
	}
}

func TestLeaseIDIsNeverUsedAsAPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "cs"))
	if err := s.Put("../../escape/attempt", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); err == nil {
		t.Fatal("the lease ID was used as a path")
	}
	names, _ := os.ReadDir(filepath.Join(dir, "cs"))
	if len(names) != 1 || strings.ContainsAny(names[0].Name(), "/\\ ") || !strings.HasSuffix(names[0].Name(), ".json") {
		t.Errorf("unexpected spool contents: %v", names)
	}
	entries, _ := s.List()
	if len(entries) != 1 || entries[0].LeaseID != "../../escape/attempt" {
		t.Errorf("the lease ID did not round-trip: %+v", entries)
	}
}

func TestOpenClearsLeftoversAndListDropsWhatCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		".tmp-123":     "half written",
		"garbage.json": "not json",
		"empty.json":   `{"lease_id":"","outcome":"e30="}`,
		"notes.txt":    "left by someone else",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("lease-1", []byte(`{"state":"scored"}`)); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.List()
	if len(entries) != 1 || entries[0].LeaseID != "lease-1" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	// The stray temporary file and the unreadable entries are gone; the
	// file that is not ours is left alone.
	if len(names) != 2 {
		t.Errorf("unexpected files left: %v", names)
	}
}

func TestFilesAreNotReadableByOthers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cs")
	s, _ := Open(dir)
	_ = s.Put("lease-1", []byte(`{}`))
	info, _ := os.Stat(dir)
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("spool folder mode %v", info.Mode().Perm())
	}
	files, _ := os.ReadDir(dir)
	fi, _ := files[0].Info()
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("spool file mode %v", fi.Mode().Perm())
	}
}

func TestPutFailsCleanlyWhenTheFolderIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cs")
	s, _ := Open(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	err := s.Put("lease-1", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("the error names a path: %v", err)
	}
}
