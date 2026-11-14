// Package tools holds Assay's sub-tools: the rules for particular cases.
//
// Each sub-tool lives in its own folder here and is listed in registry.yaml.
// The rule data is compiled into the binary, so the worker needs nothing on
// disk and runs on a read-only filesystem.
package tools

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/ClearedStack/assay/internal/yamlite"
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

// subTool is one entry of registry.yaml.
type subTool struct {
	name, path string
}

func readRegistry(data []byte) ([]subTool, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	top := root.Read("sub_tools")
	entries := top.List("sub_tools")
	if err := top.Err(); err != nil {
		return nil, err
	}
	var out []subTool
	for _, n := range entries {
		f := n.Read("name", "path", "responsibility", "parts")
		st := subTool{name: f.String("name"), path: f.String("path")}
		f.String("responsibility")
		partsNode := f.Node("parts")
		if err := f.Err(); err != nil {
			return nil, err
		}
		// A sub-tool has three parts, and the registry must say where each
		// one is.
		parts := partsNode.Read("match", "logic", "fixtures")
		for _, part := range []string{"match", "logic", "fixtures"} {
			if parts.String(part) == "" && parts.Err() == nil {
				return nil, fmt.Errorf("sub-tool %q must name its matching rule, logic and fixtures", st.name)
			}
		}
		if err := parts.Err(); err != nil {
			return nil, fmt.Errorf("sub-tool %q must name its matching rule, logic and fixtures: %w", st.name, err)
		}
		out = append(out, st)
	}
	return out, nil
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
	entries, err := readRegistry(data)
	if err != nil {
		return nil, fmt.Errorf("registry.yaml: %w", err)
	}

	var set *rubricprofiles.Set
	seen := map[string]bool{}
	for _, st := range entries {
		if seen[st.name] {
			return nil, fmt.Errorf("registry.yaml: sub-tool %q is listed twice", st.name)
		}
		seen[st.name] = true
		switch st.name {
		case rubricprofiles.Name:
			sub, err := fs.Sub(fsys, st.path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", st.path, err)
			}
			if set, err = rubricprofiles.Load(sub); err != nil {
				return nil, fmt.Errorf("%s: %w", st.path, err)
			}
		default:
			return nil, fmt.Errorf("registry.yaml: no code for sub-tool %q in this build", st.name)
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
