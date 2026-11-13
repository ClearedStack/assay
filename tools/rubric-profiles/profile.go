package rubricprofiles

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/assay/internal/contract"
	"github.com/ClearedStack/assay/internal/errs"
)

var (
	valuePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	wordPattern  = regexp.MustCompile(`^[a-z]{1,32}$`)
)

type factType string

const (
	typeBool factType = "bool"
	typeInt  factType = "int"
	typeEnum factType = "enum"
	typeText factType = "text"
	typeList factType = "list"
)

// factDef declares one fact a profile reads: its name, its type and its
// limits. A record must carry every declared fact and no other.
type factDef struct {
	typ       factType
	values    map[string]bool // enum and list
	min, max  int64           // int
	maxLength int             // text
}

// cond is one condition on one fact. Every operator present must hold.
type cond struct {
	fact      string
	typ       factType
	eqBool    *bool
	eqInt     *int64
	gte, lte  *int64
	in        map[string]bool
	anyIn     map[string]bool
	allIn     map[string]bool
	firstWord map[string]bool
}

type gateRow struct {
	when   []cond
	pass   bool
	reason string
}

type factorRow struct {
	when   []cond
	rating int
}

// Profile is one category's rules.
type Profile struct {
	ID       string
	Version  int
	Category string

	facts     map[string]factDef
	factNames []string // sorted
	gates     map[string][]gateRow
	factors   map[string][]factorRow
}

// GateDecision is the verdict of one gate. Rule is the 1-based row that
// decided it.
type GateDecision struct {
	Pass   bool
	Reason string
	Rule   int
}

type factYAML struct {
	Type      string   `yaml:"type"`
	Values    []string `yaml:"values"`
	Min       *int64   `yaml:"min"`
	Max       *int64   `yaml:"max"`
	MaxLength *int     `yaml:"max_length"`
}

type condYAML struct {
	Eq          any      `yaml:"eq"`
	Gte         *int64   `yaml:"gte"`
	Lte         *int64   `yaml:"lte"`
	In          []string `yaml:"in"`
	AnyIn       []string `yaml:"any_in"`
	AllIn       []string `yaml:"all_in"`
	FirstWordIn []string `yaml:"first_word_in"`
}

type gateRowYAML struct {
	When   map[string]condYAML `yaml:"when"`
	Pass   *bool               `yaml:"pass"`
	Reason string              `yaml:"reason"`
}

type factorRowYAML struct {
	When   map[string]condYAML `yaml:"when"`
	Rating *int                `yaml:"rating"`
}

type profileYAML struct {
	Profile string                     `yaml:"profile"`
	Version int                        `yaml:"version"`
	Facts   map[string]factYAML        `yaml:"facts"`
	Gates   map[string][]gateRowYAML   `yaml:"gates"`
	Factors map[string][]factorRowYAML `yaml:"factors"`
}

type matchYAML struct {
	Profile string `yaml:"profile"`
	Match   struct {
		ContractVersion string `yaml:"contract_version"`
		State           string `yaml:"state"`
		Category        string `yaml:"category"`
	} `yaml:"match"`
}

// parseMatch reads a matching rule and returns the category it takes.
func parseMatch(data []byte, dir string) (string, error) {
	var y matchYAML
	if err := decodeStrict(data, &y); err != nil {
		return "", err
	}
	if y.Profile != dir {
		return "", fmt.Errorf("profile is %q but the folder is %q", y.Profile, dir)
	}
	if y.Match.ContractVersion != contract.Version {
		return "", fmt.Errorf("match.contract_version must be %q", contract.Version)
	}
	if y.Match.State != contract.StateObserved {
		return "", fmt.Errorf("match.state must be %q", contract.StateObserved)
	}
	if y.Match.Category != dir {
		return "", fmt.Errorf("match.category is %q but the folder is %q", y.Match.Category, dir)
	}
	return y.Match.Category, nil
}

// parseProfile reads and checks one profile against the rubric. Every
// mistake that can be found without a record is found here.
func parseProfile(data []byte, dir string, rubric *Rubric) (*Profile, error) {
	var y profileYAML
	if err := decodeStrict(data, &y); err != nil {
		return nil, err
	}
	if y.Profile != dir {
		return nil, fmt.Errorf("profile is %q but the folder is %q", y.Profile, dir)
	}
	if y.Version < 1 {
		return nil, fmt.Errorf("version must be 1 or more")
	}
	p := &Profile{
		ID:       y.Profile,
		Version:  y.Version,
		Category: dir,
		facts:    map[string]factDef{},
		gates:    map[string][]gateRow{},
		factors:  map[string][]factorRow{},
	}

	if len(y.Facts) == 0 {
		return nil, fmt.Errorf("no facts declared")
	}
	for name, fy := range y.Facts {
		def, err := parseFact(fy)
		if err != nil {
			return nil, fmt.Errorf("fact %q: %w", name, err)
		}
		if !idPattern.MatchString(name) {
			return nil, fmt.Errorf("fact %q: invalid name", name)
		}
		p.facts[name] = def
		p.factNames = append(p.factNames, name)
	}
	sort.Strings(p.factNames)

	used := map[string]bool{}

	for _, g := range rubric.Gates {
		rows, ok := y.Gates[g.ID]
		if !ok || len(rows) == 0 {
			return nil, fmt.Errorf("gate %q has no rows", g.ID)
		}
		for i, ry := range rows {
			when, err := p.parseWhen(ry.When, used)
			if err != nil {
				return nil, fmt.Errorf("gate %q row %d: %w", g.ID, i+1, err)
			}
			if ry.Pass == nil {
				return nil, fmt.Errorf("gate %q row %d: pass is missing", g.ID, i+1)
			}
			if strings.TrimSpace(ry.Reason) == "" {
				return nil, fmt.Errorf("gate %q row %d: reason is missing", g.ID, i+1)
			}
			p.gates[g.ID] = append(p.gates[g.ID], gateRow{when: when, pass: *ry.Pass, reason: ry.Reason})
		}
	}
	if len(y.Gates) != len(rubric.Gates) {
		return nil, fmt.Errorf("gates do not match the rubric: a gate is listed that the rubric does not have")
	}

	for _, f := range rubric.Factors {
		rows, ok := y.Factors[f.ID]
		if !ok || len(rows) == 0 {
			return nil, fmt.Errorf("factor %q has no rows", f.ID)
		}
		for i, ry := range rows {
			when, err := p.parseWhen(ry.When, used)
			if err != nil {
				return nil, fmt.Errorf("factor %q row %d: %w", f.ID, i+1, err)
			}
			if ry.Rating == nil || *ry.Rating < rubric.RatingMin || *ry.Rating > rubric.RatingMax {
				return nil, fmt.Errorf("factor %q row %d: rating must be %d to %d", f.ID, i+1, rubric.RatingMin, rubric.RatingMax)
			}
			p.factors[f.ID] = append(p.factors[f.ID], factorRow{when: when, rating: *ry.Rating})
		}
	}
	if len(y.Factors) != len(rubric.Factors) {
		return nil, fmt.Errorf("factors do not match the rubric: a factor is listed that the rubric does not have")
	}

	for _, t := range rubric.TieBreakers {
		if t.Fact == "" {
			continue
		}
		def, ok := p.facts[t.Fact]
		if !ok || def.typ != typeInt {
			return nil, fmt.Errorf("tie-breaker %q needs fact %q declared as an int", t.ID, t.Fact)
		}
		used[t.Fact] = true
	}

	// A declared fact that no rule reads would be demanded from every
	// record for nothing.
	for _, name := range p.factNames {
		if !used[name] {
			return nil, fmt.Errorf("fact %q is declared but no rule reads it", name)
		}
	}
	return p, nil
}

func parseFact(fy factYAML) (factDef, error) {
	def := factDef{typ: factType(fy.Type)}
	needNone := func(what string, present bool) error {
		if present {
			return fmt.Errorf("%s does not apply to type %s", what, fy.Type)
		}
		return nil
	}
	values := func() error {
		if len(fy.Values) == 0 {
			return fmt.Errorf("values are missing")
		}
		def.values = map[string]bool{}
		for _, v := range fy.Values {
			if !valuePattern.MatchString(v) {
				return fmt.Errorf("value %q is not a lower-case token", v)
			}
			if def.values[v] {
				return fmt.Errorf("value %q is listed twice", v)
			}
			def.values[v] = true
		}
		return nil
	}
	switch def.typ {
	case typeBool:
		if err := needNone("values", fy.Values != nil); err != nil {
			return def, err
		}
		if err := needNone("min/max", fy.Min != nil || fy.Max != nil); err != nil {
			return def, err
		}
		return def, needNone("max_length", fy.MaxLength != nil)
	case typeInt:
		if fy.Min == nil || fy.Max == nil || *fy.Min > *fy.Max {
			return def, fmt.Errorf("an int needs min and max, with min no larger than max")
		}
		def.min, def.max = *fy.Min, *fy.Max
		if err := needNone("values", fy.Values != nil); err != nil {
			return def, err
		}
		return def, needNone("max_length", fy.MaxLength != nil)
	case typeEnum, typeList:
		if err := values(); err != nil {
			return def, err
		}
		if err := needNone("min/max", fy.Min != nil || fy.Max != nil); err != nil {
			return def, err
		}
		return def, needNone("max_length", fy.MaxLength != nil)
	case typeText:
		if fy.MaxLength == nil || *fy.MaxLength < 1 {
			return def, fmt.Errorf("text needs max_length of 1 or more")
		}
		def.maxLength = *fy.MaxLength
		if err := needNone("values", fy.Values != nil); err != nil {
			return def, err
		}
		return def, needNone("min/max", fy.Min != nil || fy.Max != nil)
	}
	return def, fmt.Errorf("unknown type %q", fy.Type)
}

// parseWhen checks the conditions of one row. A row with no conditions would
// be a catch-all default, which the rules forbid.
func (p *Profile) parseWhen(when map[string]condYAML, used map[string]bool) ([]cond, error) {
	if len(when) == 0 {
		return nil, fmt.Errorf("a row needs at least one condition")
	}
	names := make([]string, 0, len(when))
	for name := range when {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]cond, 0, len(names))
	for _, name := range names {
		def, ok := p.facts[name]
		if !ok {
			return nil, fmt.Errorf("condition on undeclared fact %q", name)
		}
		c, err := parseCond(name, def, when[name])
		if err != nil {
			return nil, fmt.Errorf("fact %q: %w", name, err)
		}
		used[name] = true
		out = append(out, c)
	}
	return out, nil
}

func parseCond(name string, def factDef, cy condYAML) (cond, error) {
	c := cond{fact: name, typ: def.typ}
	has := map[string]bool{
		"eq":            cy.Eq != nil,
		"gte":           cy.Gte != nil,
		"lte":           cy.Lte != nil,
		"in":            cy.In != nil,
		"any_in":        cy.AnyIn != nil,
		"all_in":        cy.AllIn != nil,
		"first_word_in": cy.FirstWordIn != nil,
	}
	allowed := map[factType][]string{
		typeBool: {"eq"},
		typeInt:  {"eq", "gte", "lte"},
		typeEnum: {"in"},
		typeText: {"first_word_in"},
		typeList: {"any_in", "all_in"},
	}[def.typ]
	count := 0
	for _, op := range []string{"eq", "gte", "lte", "in", "any_in", "all_in", "first_word_in"} {
		if !has[op] {
			continue
		}
		count++
		ok := false
		for _, a := range allowed {
			ok = ok || a == op
		}
		if !ok {
			return c, fmt.Errorf("operator %s does not apply to type %s", op, def.typ)
		}
	}
	if count == 0 {
		return c, fmt.Errorf("no operator")
	}

	subset := func(list []string) (map[string]bool, error) {
		if len(list) == 0 {
			return nil, fmt.Errorf("an empty list can never match")
		}
		set := map[string]bool{}
		for _, v := range list {
			if !def.values[v] {
				return nil, fmt.Errorf("value %q is not among the declared values", v)
			}
			if set[v] {
				return nil, fmt.Errorf("value %q is listed twice", v)
			}
			set[v] = true
		}
		return set, nil
	}
	inRange := func(v int64) error {
		if v < def.min || v > def.max {
			return fmt.Errorf("%d is outside the declared range %d to %d", v, def.min, def.max)
		}
		return nil
	}

	var err error
	switch def.typ {
	case typeBool:
		b, ok := cy.Eq.(bool)
		if !ok {
			return c, fmt.Errorf("eq must be true or false")
		}
		c.eqBool = &b
	case typeInt:
		if cy.Eq != nil {
			i, ok := cy.Eq.(int)
			if !ok {
				return c, fmt.Errorf("eq must be a whole number")
			}
			if cy.Gte != nil || cy.Lte != nil {
				return c, fmt.Errorf("eq cannot be combined with gte or lte")
			}
			v := int64(i)
			if err := inRange(v); err != nil {
				return c, err
			}
			c.eqInt = &v
		}
		if cy.Gte != nil {
			if err := inRange(*cy.Gte); err != nil {
				return c, err
			}
			c.gte = cy.Gte
		}
		if cy.Lte != nil {
			if err := inRange(*cy.Lte); err != nil {
				return c, err
			}
			c.lte = cy.Lte
		}
		if c.gte != nil && c.lte != nil && *c.gte > *c.lte {
			return c, fmt.Errorf("gte is larger than lte, so the row can never match")
		}
	case typeEnum:
		if c.in, err = subset(cy.In); err != nil {
			return c, err
		}
	case typeList:
		if cy.AnyIn != nil {
			if c.anyIn, err = subset(cy.AnyIn); err != nil {
				return c, err
			}
		}
		if cy.AllIn != nil {
			if c.allIn, err = subset(cy.AllIn); err != nil {
				return c, err
			}
		}
	case typeText:
		if len(cy.FirstWordIn) == 0 {
			return c, fmt.Errorf("an empty list can never match")
		}
		c.firstWord = map[string]bool{}
		for _, w := range cy.FirstWordIn {
			if !wordPattern.MatchString(w) {
				return c, fmt.Errorf("word %q must be lower-case letters only", w)
			}
			if c.firstWord[w] {
				return c, fmt.Errorf("word %q is listed twice", w)
			}
			c.firstWord[w] = true
		}
	}
	return c, nil
}

// CheckFacts holds a record's facts against the profile's declarations.
//
// Broken data comes first and gives E_PARSE: a declared fact that is missing,
// has the wrong type or is out of range. Only then is the unfamiliar
// reported, with E_NEEDS_SUBTOOL: a fact the profile does not declare, or a
// value outside a declared set. Facts are checked in name order, so the same
// record always reports the same error.
func (p *Profile) CheckFacts(facts map[string]contract.Value) *errs.Error {
	for _, name := range p.factNames {
		def := p.facts[name]
		v, ok := facts[name]
		if !ok {
			return errs.New(errs.Parse, errs.MissingFact, name, p.Category)
		}
		switch def.typ {
		case typeBool:
			if v.Kind != contract.KindBool {
				return errs.New(errs.Parse, errs.InvalidType, name, p.Category)
			}
		case typeInt:
			if v.Kind != contract.KindInt {
				return errs.New(errs.Parse, errs.InvalidType, name, p.Category)
			}
			if v.Int < def.min || v.Int > def.max {
				return errs.New(errs.Parse, errs.OutOfRange, name, p.Category)
			}
		case typeEnum:
			if v.Kind != contract.KindString {
				return errs.New(errs.Parse, errs.InvalidType, name, p.Category)
			}
		case typeText:
			if v.Kind != contract.KindString {
				return errs.New(errs.Parse, errs.InvalidType, name, p.Category)
			}
			if !validText(v.Str, def.maxLength) {
				return errs.New(errs.Parse, errs.InvalidValue, name, p.Category)
			}
		case typeList:
			if v.Kind != contract.KindList {
				return errs.New(errs.Parse, errs.InvalidType, name, p.Category)
			}
			if len(v.List) == 0 || hasDuplicate(v.List) {
				return errs.New(errs.Parse, errs.InvalidValue, name, p.Category)
			}
		}
	}

	var unknown []string
	for name := range facts {
		if _, ok := p.facts[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errs.New(errs.NeedsSubtool, errs.UnknownFact, "facts", p.Category, unknown[0])
	}

	for _, name := range p.factNames {
		def := p.facts[name]
		v := facts[name]
		switch def.typ {
		case typeEnum:
			if !def.values[v.Str] {
				return errs.New(errs.NeedsSubtool, errs.UnknownValue, name, p.Category, v.Str)
			}
		case typeList:
			items := append([]string(nil), v.List...)
			sort.Strings(items)
			for _, item := range items {
				if !def.values[item] {
					return errs.New(errs.NeedsSubtool, errs.UnknownValue, name, p.Category, item)
				}
			}
		}
	}
	return nil
}

func validText(s string, maxLength int) bool {
	if strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > maxLength {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func hasDuplicate(list []string) bool {
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		if seen[s] {
			return true
		}
		seen[s] = true
	}
	return false
}

// Gate decides one knockout gate. ok is false when no row matches, in which
// case there is no verdict. Call CheckFacts first.
func (p *Profile) Gate(id string, facts map[string]contract.Value) (d GateDecision, ok bool) {
	for i, row := range p.gates[id] {
		if matches(row.when, facts) {
			return GateDecision{Pass: row.pass, Reason: row.reason, Rule: i + 1}, true
		}
	}
	return GateDecision{}, false
}

// Factor rates one rubric factor. rule is the 1-based row that decided it.
// ok is false when no row matches. Call CheckFacts first.
func (p *Profile) Factor(id string, facts map[string]contract.Value) (rating, rule int, ok bool) {
	for i, row := range p.factors[id] {
		if matches(row.when, facts) {
			return row.rating, i + 1, true
		}
	}
	return 0, 0, false
}

// GateRows and FactorRows give the size of a decision table. The fixture
// tests use them to prove that every row has a saved example.
func (p *Profile) GateRows(id string) int   { return len(p.gates[id]) }
func (p *Profile) FactorRows(id string) int { return len(p.factors[id]) }

func matches(when []cond, facts map[string]contract.Value) bool {
	for _, c := range when {
		if !c.holds(facts[c.fact]) {
			return false
		}
	}
	return true
}

func (c cond) holds(v contract.Value) bool {
	switch c.typ {
	case typeBool:
		return v.Bool == *c.eqBool
	case typeInt:
		if c.eqInt != nil && v.Int != *c.eqInt {
			return false
		}
		if c.gte != nil && v.Int < *c.gte {
			return false
		}
		if c.lte != nil && v.Int > *c.lte {
			return false
		}
		return true
	case typeEnum:
		return c.in[v.Str]
	case typeText:
		return c.firstWord[firstWord(v.Str)]
	case typeList:
		if c.anyIn != nil {
			found := false
			for _, item := range v.List {
				found = found || c.anyIn[item]
			}
			if !found {
				return false
			}
		}
		if c.allIn != nil {
			for _, item := range v.List {
				if !c.allIn[item] {
					return false
				}
			}
		}
		return true
	}
	return false
}

// firstWord returns the leading run of ASCII letters in s, lower-cased, after
// any leading spaces. Text that opens with anything else has no first word
// and so matches no list.
func firstWord(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	end := 0
	for end < len(s) {
		ch := s[end]
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') {
			break
		}
		end++
	}
	return strings.ToLower(s[:end])
}
