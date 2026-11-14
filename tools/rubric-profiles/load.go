package rubricprofiles

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

// Name is this sub-tool's name in tools/registry.yaml.
const Name = "rubric-profiles"

// fixturesDir is the one folder beside the categories that is not a
// category: it holds fixtures for the matching rule itself.
const fixturesDir = "fixtures"

// Set is the loaded sub-tool: the shared rubric and one profile per
// category.
type Set struct {
	Rubric     *Rubric
	profiles   map[string]*Profile
	categories []string
}

// Load reads rubric.yaml and every <category>/match.yaml and
// <category>/profile.yaml under fsys, and checks them all. Any mistake in
// the rule data is an error here, at start-up, and never a surprise later.
func Load(fsys fs.FS) (*Set, error) {
	data, err := fs.ReadFile(fsys, "rubric.yaml")
	if err != nil {
		return nil, fmt.Errorf("rubric.yaml: %w", err)
	}
	rubric, err := parseRubric(data)
	if err != nil {
		return nil, fmt.Errorf("rubric.yaml: %w", err)
	}
	set := &Set{Rubric: rubric, profiles: map[string]*Profile{}}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == fixturesDir {
			continue
		}
		dir := entry.Name()
		matchData, err := fs.ReadFile(fsys, dir+"/match.yaml")
		if err != nil {
			return nil, fmt.Errorf("%s/match.yaml: %w", dir, err)
		}
		category, err := parseMatch(matchData, dir)
		if err != nil {
			return nil, fmt.Errorf("%s/match.yaml: %w", dir, err)
		}
		profileData, err := fs.ReadFile(fsys, dir+"/profile.yaml")
		if err != nil {
			return nil, fmt.Errorf("%s/profile.yaml: %w", dir, err)
		}
		profile, err := parseProfile(profileData, dir, rubric)
		if err != nil {
			return nil, fmt.Errorf("%s/profile.yaml: %w", dir, err)
		}
		set.profiles[category] = profile
		set.categories = append(set.categories, category)
	}
	if len(set.categories) == 0 {
		return nil, errors.New("no profiles found")
	}
	sort.Strings(set.categories)
	return set, nil
}

// Match applies the matching rule: it returns the profile that takes records
// of this category, or nil when there is none.
func (s *Set) Match(category string) *Profile {
	return s.profiles[category]
}

// Categories lists the categories that have a profile, in name order.
func (s *Set) Categories() []string {
	return append([]string(nil), s.categories...)
}
