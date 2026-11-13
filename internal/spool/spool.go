// Package spool keeps finished outcomes on disk when Muster cannot take
// them, so that a hub restart loses no work.
//
// The spool directory (/var/spool/cs in the cluster) is the only place Assay
// ever writes. Each entry is one small file, written whole and then renamed
// into place, so a crash can leave a stray temporary file but never half an
// entry.
package spool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	entrySuffix = ".json"
	tempPrefix  = ".tmp-"
	// maxEntryBytes bounds one entry when it is read back.
	maxEntryBytes = 2 << 20
)

// Spool is a directory of outcomes waiting to be handed to the hub.
type Spool struct {
	dir string
}

// Entry is one waiting outcome. The outcome is stored as opaque bytes
// (base64 in the file) so that what is delivered later is exactly what was
// produced, to the byte.
type Entry struct {
	LeaseID string `json:"lease_id"`
	Outcome []byte `json:"outcome"`
	name    string
}

// Open prepares the spool in dir, creating it if need be, and clears
// temporary files left by an earlier crash.
func Open(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("the spool directory could not be created")
	}
	names, err := readNames(dir)
	if err != nil {
		return nil, errors.New("the spool directory could not be read")
	}
	for _, name := range names {
		if strings.HasPrefix(name, tempPrefix) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return &Spool{dir: dir}, nil
}

// Put saves an outcome for a lease. Saving the same lease twice keeps the
// later outcome.
func (s *Spool) Put(leaseID string, outcome []byte) error {
	data, err := json.Marshal(Entry{LeaseID: leaseID, Outcome: outcome})
	if err != nil {
		return errors.New("the outcome could not be spooled")
	}
	name := fileName(leaseID)
	tmp, err := os.CreateTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return errors.New("the outcome could not be spooled")
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.New("the outcome could not be spooled")
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmp.Name())
		return errors.New("the outcome could not be spooled")
	}
	return nil
}

// List returns the waiting entries in a fixed order. A file that cannot be
// read back as an entry is removed: it can never be delivered, and the hub
// will lease its record again.
func (s *Spool) List() ([]Entry, error) {
	names, err := readNames(s.dir)
	if err != nil {
		return nil, errors.New("the spool directory could not be read")
	}
	var entries []Entry
	for _, name := range names {
		if strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, entrySuffix) {
			continue
		}
		path := filepath.Join(s.dir, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		var e Entry
		data, err := os.ReadFile(path)
		if info.Size() > maxEntryBytes || err != nil || json.Unmarshal(data, &e) != nil || e.LeaseID == "" || len(e.Outcome) == 0 {
			_ = os.Remove(path)
			continue
		}
		e.name = name
		entries = append(entries, e)
	}
	return entries, nil
}

// Remove deletes an entry once the hub has taken it.
func (s *Spool) Remove(e Entry) error {
	if e.name == "" {
		e.name = fileName(e.LeaseID)
	}
	if err := os.Remove(filepath.Join(s.dir, e.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("a spooled outcome could not be removed")
	}
	return nil
}

// fileName derives a safe file name from a lease ID. The ID comes from the
// hub, so it is hashed instead of being trusted as a path.
func fileName(leaseID string) string {
	sum := sha256.Sum256([]byte(leaseID))
	return hex.EncodeToString(sum[:16]) + entrySuffix
}

func readNames(dir string) ([]string, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name())
	}
	sort.Strings(names)
	return names, nil
}
