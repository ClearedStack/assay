// Package tools holds Assay's sub-tools: the rules for particular cases.
//
// Each sub-tool lives in its own folder here and is listed in registry.yaml.
// The rule data is compiled into the binary, so the worker needs nothing on
// disk and runs on a read-only filesystem.
package tools

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"go.yaml.in/yaml/v3"

	rubricprofiles "github.com/ClearedStack/assay/tools/rubric-profiles"
)

// Only the matching rules and the logic are embedded. Fixtures stay out of
// the binary.
//
//go:embed registry.yaml
//go:embed rubric-profiles/rubric.yaml
//go:embed rubric-profiles/*/match.yaml
//go:embed rubric-profiles/*/profile.yaml
var embedded embed.FS

// FS returns the embedded rule data, rooted at the tools folder.
func FS() fs.FS { return embedded }

type registryYAML struct {
	SubTools []struct {
		Name           string `yaml:"name"`
		Path           string `yaml:"path"`
		Responsibility string `yaml:"responsibility"`
		Parts          struct {
			Match    string `yaml:"match"`
			Logic    string `yaml:"logic"`
			Fixtures string `yaml:"fixtures"`
		} `yaml:"parts"`
	} `yaml:"sub_tools"`
}

// Load reads registry.yaml from fsys and loads every sub-tool it lists.
//
// A sub-tool the registry lists but this build has no code for is an error:
// the binary and the registry must agree. Today there is one sub-tool,
// rubric-profiles, and it is required.
func Load(fsys fs.FS) (*rubricprofiles.Set, error) {
	data, err := fs.ReadFile(fsys, "registry.yaml")
	if err != nil {
		return nil, fmt.Errorf("registry.yaml: %w", err)
	}
	var reg registryYAML
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&reg); err != nil && err != io.EOF {
		return nil, fmt.Errorf("registry.yaml: %w", err)
	}

	var set *rubricprofiles.Set
	seen := map[string]bool{}
	for _, st := range reg.SubTools {
		if seen[st.Name] {
			return nil, fmt.Errorf("registry.yaml: sub-tool %q is listed twice", st.Name)
		}
		seen[st.Name] = true
		if st.Parts.Match == "" || st.Parts.Logic == "" || st.Parts.Fixtures == "" {
			return nil, fmt.Errorf("registry.yaml: sub-tool %q must name its matching rule, logic and fixtures", st.Name)
		}
		switch st.Name {
		case rubricprofiles.Name:
			sub, err := fs.Sub(fsys, st.Path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", st.Path, err)
			}
			if set, err = rubricprofiles.Load(sub); err != nil {
				return nil, fmt.Errorf("%s: %w", st.Path, err)
			}
		default:
			return nil, fmt.Errorf("registry.yaml: no code for sub-tool %q in this build", st.Name)
		}
	}
	if set == nil {
		return nil, errors.New("registry.yaml: sub-tool " + rubricprofiles.Name + " is not listed")
	}
	return set, nil
}

// LoadEmbedded loads the rules compiled into the binary.
func LoadEmbedded() (*rubricprofiles.Set, error) {
	return Load(embedded)
}
