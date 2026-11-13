// Package assay_test checks the rules that apply to the repository as a
// whole rather than to any one package: how it is deployed, what it depends
// on, and that its names agree with the registry.
package assay_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ClearedStack/assay/internal/yamlite"
)

type component struct {
	Slug      string   `json:"slug"`
	Namespace string   `json:"namespace"`
	Image     string   `json:"image"`
	Memory    string   `json:"memory"`
	Version   string   `json:"version"`
	SubTools  []string `json:"subTools"`
}

func readComponent(t *testing.T) component {
	t.Helper()
	data, err := os.ReadFile("component.json")
	if err != nil {
		t.Fatal(err)
	}
	var c component
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func readManifests(t *testing.T) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile("deploy/assay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := yamlite.ParseAll(data)
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{}
	for _, n := range parsed {
		doc := n.Value().(map[string]any)
		docs[doc["kind"].(string)] = doc
	}
	if len(docs) != 3 {
		t.Fatalf("want a ServiceAccount, a Deployment and a NetworkPolicy, got %d documents", len(docs))
	}
	return docs
}

// at walks nested maps and lists: at(doc, "spec", "containers", 0, "name").
func at(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("no %q on the way to %v", s, path)
			}
			v = m[s]
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				t.Fatalf("no item %d on the way to %v", s, path)
			}
			v = l[s]
		}
	}
	return v
}

// TestPodHardening is the "Pod hardening & network policy" rule of SPEC.md,
// one assertion per clause.
func TestPodHardening(t *testing.T) {
	c := readComponent(t)
	docs := readManifests(t)
	dep := docs["Deployment"]
	pod := at(t, dep, "spec", "template", "spec")
	ctr := at(t, pod, "containers", 0)

	for kind, doc := range docs {
		if at(t, doc, "metadata", "name") != c.Slug || at(t, doc, "metadata", "namespace") != c.Namespace {
			t.Errorf("%s: name and namespace must be %s in %s", kind, c.Slug, c.Namespace)
		}
	}

	// Unprivileged, non-root.
	if at(t, pod, "securityContext", "runAsNonRoot") != true {
		t.Error("the pod must run as non-root")
	}
	if uid, _ := at(t, pod, "securityContext", "runAsUser").(int); uid == 0 {
		t.Error("the pod must not run as user 0")
	}
	if at(t, ctr, "securityContext", "privileged") != false || at(t, ctr, "securityContext", "allowPrivilegeEscalation") != false {
		t.Error("the container must be unprivileged and unable to gain privileges")
	}
	// Dropped capabilities.
	if drop := at(t, ctr, "securityContext", "capabilities", "drop").([]any); len(drop) != 1 || drop[0] != "ALL" {
		t.Errorf("all capabilities must be dropped, got %v", drop)
	}
	if _, added := at(t, ctr, "securityContext", "capabilities").(map[string]any)["add"]; added {
		t.Error("no capability may be added")
	}
	// Read-only root filesystem, writable only at /var/spool/cs.
	if at(t, ctr, "securityContext", "readOnlyRootFilesystem") != true {
		t.Error("the root filesystem must be read-only")
	}
	mounts := at(t, ctr, "volumeMounts").([]any)
	if len(mounts) != 1 || at(t, mounts[0], "mountPath") != "/var/spool/cs" {
		t.Errorf("the only mount must be /var/spool/cs, got %v", mounts)
	}
	volumes := at(t, pod, "volumes").([]any)
	if len(volumes) != 1 || at(t, volumes[0], "emptyDir") == nil {
		t.Errorf("the only volume must be the spool, got %v", volumes)
	}
	// Memory budget.
	if c.Memory != "30 MiB" || at(t, ctr, "resources", "limits", "memory") != "30Mi" {
		t.Errorf("the memory limit must be the %s budget", c.Memory)
	}
	// Zero database credentials: no secret, no env taken from anywhere.
	if _, ok := ctr.(map[string]any)["envFrom"]; ok {
		t.Error("the container must not take its environment from a secret or config map")
	}
	names := map[string]bool{}
	for _, e := range at(t, ctr, "env").([]any) {
		entry := e.(map[string]any)
		name := entry["name"].(string)
		names[name] = true
		if _, ok := entry["valueFrom"]; ok {
			t.Errorf("env %s is taken from another object", name)
		}
		if regexp.MustCompile(`(?i)password|secret|token|key|dsn|database`).MatchString(name) {
			t.Errorf("env %s looks like a credential", name)
		}
	}
	if !names["CONTRACT_VERSION"] || !names["MUSTER_URL"] {
		t.Error("CONTRACT_VERSION and MUSTER_URL must be set")
	}
	// Built with ko.
	if image := at(t, ctr, "image").(string); image != "ko://github.com/ClearedStack/assay/cmd/assay" {
		t.Errorf("the image must be a ko reference to the command, got %q", image)
	}
	if !strings.HasSuffix("github.com/ClearedStack/"+c.Image, "/"+c.Slug) {
		t.Error("the image name must be the component's slug")
	}
	if args := at(t, ctr, "args").([]any); len(args) != 1 || args[0] != "run" {
		t.Errorf("the container must run the worker, got %v", args)
	}
}

func TestNetworkPolicyIsDefaultDenyWithOneWayOut(t *testing.T) {
	np := readManifests(t)["NetworkPolicy"]
	types := at(t, np, "spec", "policyTypes").([]any)
	if len(types) != 2 || types[0] != "Ingress" || types[1] != "Egress" {
		t.Errorf("both directions must be governed, got %v", types)
	}
	if in := at(t, np, "spec", "ingress").([]any); len(in) != 0 {
		t.Errorf("nothing may connect in, got %v", in)
	}
	egress := at(t, np, "spec", "egress").([]any)
	if len(egress) != 2 {
		t.Fatalf("want two ways out (Muster, and looking up its name), got %d", len(egress))
	}
	if at(t, egress[0], "to", 0, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "clearedstack" ||
		at(t, egress[0], "to", 0, "podSelector", "matchLabels", "app.kubernetes.io/name") != "muster" {
		t.Error("the first way out must be Muster in the clearedstack namespace")
	}
	if ports := at(t, egress[0], "ports").([]any); len(ports) != 1 || at(t, ports[0], "protocol") != "TCP" {
		t.Errorf("Muster is reached on one TCP port, got %v", ports)
	}
	for _, p := range at(t, egress[1], "ports").([]any) {
		if at(t, p, "port") != 53 {
			t.Errorf("the second way out is for name lookups only, got %v", p)
		}
	}
	for _, rule := range egress {
		for _, peer := range at(t, rule, "to").([]any) {
			m := peer.(map[string]any)
			if m["ipBlock"] != nil || m["namespaceSelector"] == nil || m["podSelector"] == nil {
				t.Errorf("each way out must name both a namespace and its pods, got %v", m)
			}
		}
	}
}

func TestNoDockerfile(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		name := strings.ToLower(d.Name())
		if strings.HasPrefix(name, "dockerfile") || strings.HasSuffix(name, ".dockerfile") || name == ".dockerignore" || strings.HasPrefix(name, "docker-compose") {
			t.Errorf("%s: the image is built with ko; there must be no Docker files", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ko, err := os.ReadFile(".ko.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := yamlite.Parse(ko)
	if err != nil {
		t.Fatalf(".ko.yaml: %v", err)
	}
	builds, _ := cfg.Value().(map[string]any)["builds"].([]any)
	if len(builds) != 1 || at(t, builds[0], "main") != "./cmd/assay" {
		t.Errorf(".ko.yaml must build ./cmd/assay and nothing else")
	}
}

// TestStandardLibraryOnly holds the repository to having no dependency at
// all: go.mod requires nothing, and no Go file imports a package from
// outside the standard library and this module.
func TestStandardLibraryOnly(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gomod), "require") {
		t.Errorf("go.mod must require nothing:\n%s", gomod)
	}
	const module = "github.com/ClearedStack/assay"
	if !strings.Contains(string(gomod), "module "+module+"\n") {
		t.Fatalf("unexpected module path in go.mod")
	}
	importLine := regexp.MustCompile(`^\s*(?:import\s+)?(?:[A-Za-z_.][A-Za-z0-9_]*\s+)?"([^"]+)"\s*$`)
	files := 0
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "third_party") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inBlock := false
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "import (":
				inBlock = true
				continue
			case inBlock && trimmed == ")":
				inBlock = false
				continue
			case !inBlock && !strings.HasPrefix(trimmed, "import "):
				continue
			}
			m := importLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			first := strings.Split(m[1], "/")[0]
			if strings.Contains(first, ".") && !strings.HasPrefix(m[1], module) {
				t.Errorf("%s imports %q, which is outside the standard library", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 20 {
		t.Fatalf("only %d Go files were checked", files)
	}
	license, err := os.ReadFile("LICENSE")
	if err != nil || !strings.HasPrefix(string(license), "MIT License") {
		t.Error("the repository must keep its MIT LICENSE file")
	}
}

func TestNamesComeFromTheRegistry(t *testing.T) {
	c := readComponent(t)
	if c.Slug != "assay" || c.Namespace != "cs-workers" {
		t.Fatalf("component.json is not this component's entry: %+v", c)
	}
	main, err := os.ReadFile("cmd/assay/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `const version = "`+c.Version+`"`) {
		t.Errorf("the command's version must be %s, as in component.json", c.Version)
	}
	outcome, err := os.ReadFile("internal/contract/outcome.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(outcome), `const Worker = "`+c.Slug+`"`) {
		t.Errorf("the worker name must be %s, as in component.json", c.Slug)
	}
	registry, err := os.ReadFile("tools/registry.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type entry struct{ Name, Path string }
	var reg struct{ SubTools []entry }
	root, err := yamlite.Parse(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range root.Read("sub_tools").List("sub_tools") {
		reg.SubTools = append(reg.SubTools, entry{Name: n.Map["name"].Str, Path: n.Map["path"].Str})
	}
	if len(reg.SubTools) != len(c.SubTools) {
		t.Fatalf("tools/registry.yaml lists %d sub-tools, component.json %d", len(reg.SubTools), len(c.SubTools))
	}
	for i, name := range c.SubTools {
		if reg.SubTools[i].Name != name {
			t.Errorf("sub-tool %d: registry.yaml says %q, component.json says %q", i, reg.SubTools[i].Name, name)
		}
		if info, err := os.Stat(filepath.Join("tools", reg.SubTools[i].Path)); err != nil || !info.IsDir() {
			t.Errorf("sub-tool %q has no folder under tools/", name)
		}
	}
}
