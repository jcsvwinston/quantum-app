// Command suitemanifest builds and gates suite-manifest.yaml: the honest map
// of which certified-suite surfaces this app executes.
//
// The DENOMINATOR is never invented here — it is read from the inventories
// the suite repos already publish, at the exact versions pinned in go.mod,
// resolved through the Go module cache (so the tool always describes the tags
// the app really builds against):
//
//   - quark:   examples/superapp/apisurface.json   (S7 surface manifest)
//   - nucleus: docs/reference/API_CONTRACT_INVENTORY.md (public package table)
//   - orbit:   contracts/baseline/api_exported_symbols.txt (frozen v1 surface)
//
// Modes:
//
//	-mode gen    print the denominator and, when suite-manifest.yaml exists,
//	             the items it leaves unclassified (authoring aid).
//	-mode check  gate: exit 1 when any denominator item is unclassified, when
//	             an exact classification names a non-existent item, when a
//	             pattern matches nothing (stale rule), or when the manifest
//	             pins disagree with go.mod.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

const manifestFile = "suite-manifest.yaml"

type classification struct {
	Match    string `yaml:"match"`
	Status   string `yaml:"status"` // covered | not-covered | out-of-scope
	Evidence string `yaml:"evidence,omitempty"`
	Reason   string `yaml:"reason,omitempty"`
}

type manifest struct {
	Suite           string            `yaml:"suite"`
	Pins            map[string]string `yaml:"pins"`
	Sources         map[string]string `yaml:"sources"`
	Notes           string            `yaml:"notes,omitempty"`
	Classifications []classification  `yaml:"classifications"`
}

type item struct {
	ID   string
	Kind string // informational
}

func main() {
	mode := flag.String("mode", "check", "gen | check")
	flag.Parse()

	if err := run(*mode); err != nil {
		fmt.Fprintf(os.Stderr, "suitemanifest: %v\n", err)
		os.Exit(1)
	}
}

func run(mode string) error {
	items, pins, err := buildDenominator()
	if err != nil {
		return err
	}

	switch mode {
	case "gen":
		return genMode(items, pins)
	case "check":
		return checkMode(items, pins)
	default:
		return fmt.Errorf("unknown -mode %q (want gen or check)", mode)
	}
}

// ---- denominator -----------------------------------------------------------

func buildDenominator() ([]item, map[string]string, error) {
	quarkDir, quarkVer, err := moduleDir("github.com/jcsvwinston/quark")
	if err != nil {
		return nil, nil, err
	}
	nucleusDir, nucleusVer, err := moduleDir("github.com/jcsvwinston/nucleus")
	if err != nil {
		return nil, nil, err
	}
	orbitDir, orbitVer, err := moduleDir("github.com/jcsvwinston/orbit")
	if err != nil {
		return nil, nil, err
	}

	var items []item

	q, err := quarkItems(filepath.Join(quarkDir, "examples", "superapp", "apisurface.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("quark inventory: %w", err)
	}
	items = append(items, q...)

	n, err := nucleusItems(filepath.Join(nucleusDir, "docs", "reference", "API_CONTRACT_INVENTORY.md"))
	if err != nil {
		return nil, nil, fmt.Errorf("nucleus inventory: %w", err)
	}
	items = append(items, n...)

	o, err := orbitItems(filepath.Join(orbitDir, "contracts", "baseline", "api_exported_symbols.txt"))
	if err != nil {
		return nil, nil, fmt.Errorf("orbit inventory: %w", err)
	}
	items = append(items, o...)

	pins := map[string]string{
		"quark":   quarkVer,
		"nucleus": nucleusVer,
		"orbit":   orbitVer,
	}
	return items, pins, nil
}

// moduleDir resolves the on-disk module cache directory and version of a
// dependency at the version pinned in go.mod (GOWORK=off: pins only).
func moduleDir(mod string) (dir, version string, err error) {
	cmd := exec.Command("go", "mod", "download", "-json", mod)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("go mod download -json %s: %w", mod, err)
	}
	var info struct {
		Version string
		Dir     string
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", "", fmt.Errorf("parse go mod download output for %s: %w", mod, err)
	}
	if info.Dir == "" {
		return "", "", fmt.Errorf("no module cache dir for %s", mod)
	}
	return info.Dir, info.Version, nil
}

// quarkItems reads quark's S7 surface manifest (generated upstream by
// cmd/gen-apisurface; JSON {symbols:[{pkg,name,kind}]}).
func quarkItems(p string) ([]item, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Symbols []struct {
			Pkg  string `json:"pkg"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	items := make([]item, 0, len(doc.Symbols))
	for _, s := range doc.Symbols {
		items = append(items, item{
			ID:   "quark:" + s.Pkg + "." + s.Name,
			Kind: s.Kind,
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no symbols in %s", p)
	}
	return items, nil
}

// nucleusItems parses the "Public Package Inventory" markdown table: one
// denominator item per public package row (`pkg/...`), which is exactly the
// granularity nucleus freezes its contract at.
func nucleusItems(p string) ([]item, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var items []item
	inSection := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = strings.Contains(trimmed, "Public Package Inventory")
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 3 {
			continue
		}
		surface := strings.TrimSpace(cells[1])
		surface = strings.Trim(surface, "`~ ")
		if !strings.HasPrefix(surface, "pkg/") {
			continue
		}
		lifecycle := strings.Trim(strings.TrimSpace(cells[2]), "` ")
		if lifecycle == "removed" {
			continue
		}
		items = append(items, item{ID: "nucleus:" + surface, Kind: "package"})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no package rows parsed from %s", p)
	}
	return items, nil
}

// orbitItems reads orbit's frozen-surface baseline (one "pkg kind:name" line
// per exported symbol of the root and datasource packages).
func orbitItems(p string) ([]item, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var items []item
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected baseline line %q", trimmed)
		}
		kind, _, _ := strings.Cut(fields[1], ":")
		items = append(items, item{ID: "orbit:" + fields[0] + " " + fields[1], Kind: kind})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no symbols in %s", p)
	}
	return items, nil
}

// ---- classification --------------------------------------------------------

func loadManifest() (*manifest, error) {
	raw, err := os.ReadFile(manifestFile)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestFile, err)
	}
	return &m, nil
}

// classify resolves every item against the classification list: exact matches
// win over patterns; among patterns, first match (file order) wins. Patterns
// use '*' as "any sequence of characters" (it crosses '/' and '.'); every
// other character is literal. Returns the per-item classification index
// (-1 = unclassified) and per-rule match counts.
func classify(items []item, rules []classification) (assignment []int, ruleHits []int) {
	assignment = make([]int, len(items))
	ruleHits = make([]int, len(rules))

	exact := map[string]int{}
	type pat struct {
		idx int
		re  *regexp.Regexp
	}
	var patterns []pat
	for i, r := range rules {
		if strings.Contains(r.Match, "*") {
			parts := strings.Split(r.Match, "*")
			for j, p := range parts {
				parts[j] = regexp.QuoteMeta(p)
			}
			re := regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
			patterns = append(patterns, pat{idx: i, re: re})
		} else {
			exact[r.Match] = i
		}
	}

	for idx, it := range items {
		assignment[idx] = -1
		if ri, ok := exact[it.ID]; ok {
			assignment[idx] = ri
			ruleHits[ri]++
			continue
		}
		for _, p := range patterns {
			if p.re.MatchString(it.ID) {
				assignment[idx] = p.idx
				ruleHits[p.idx]++
				break
			}
		}
	}
	return assignment, ruleHits
}

// ---- modes -----------------------------------------------------------------

func genMode(items []item, pins map[string]string) error {
	fmt.Printf("# denominator: %d items (quark %s · nucleus %s · orbit %s)\n",
		len(items), pins["quark"], pins["nucleus"], pins["orbit"])
	for _, it := range items {
		fmt.Println(it.ID)
	}

	if _, err := os.Stat(manifestFile); err == nil {
		m, err := loadManifest()
		if err != nil {
			return err
		}
		assignment, _ := classify(items, m.Classifications)
		var missing []string
		for i, a := range assignment {
			if a == -1 {
				missing = append(missing, items[i].ID)
			}
		}
		fmt.Fprintf(os.Stderr, "# unclassified against %s: %d\n", manifestFile, len(missing))
		for _, id := range missing {
			fmt.Fprintln(os.Stderr, "#   "+id)
		}
	}
	return nil
}

func checkMode(items []item, pins map[string]string) error {
	m, err := loadManifest()
	if err != nil {
		return err
	}

	var problems []string

	// Pins in the manifest must match what go.mod resolves.
	for mod, ver := range pins {
		if m.Pins[mod] != ver {
			problems = append(problems, fmt.Sprintf(
				"pin drift: manifest says %s %s but go.mod resolves %s", mod, m.Pins[mod], ver))
		}
	}

	// Every classification must be well-formed.
	for _, r := range m.Classifications {
		switch r.Status {
		case "covered":
			if strings.TrimSpace(r.Evidence) == "" {
				problems = append(problems, fmt.Sprintf("covered without evidence: %s", r.Match))
			}
		case "not-covered", "out-of-scope":
			if strings.TrimSpace(r.Reason) == "" {
				problems = append(problems, fmt.Sprintf("%s without reason: %s", r.Status, r.Match))
			}
		default:
			problems = append(problems, fmt.Sprintf("unknown status %q for %s", r.Status, r.Match))
		}
	}

	assignment, ruleHits := classify(items, m.Classifications)

	// Gate 1: no denominator item may be left unclassified.
	var unclassified []string
	for i, a := range assignment {
		if a == -1 {
			unclassified = append(unclassified, items[i].ID)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		max := len(unclassified)
		if max > 25 {
			max = 25
		}
		problems = append(problems, fmt.Sprintf("%d denominator item(s) unclassified, e.g.:", len(unclassified)))
		for _, id := range unclassified[:max] {
			problems = append(problems, "  UNCLASSIFIED "+id)
		}
	}

	// Gate 2: no orphan classification (exact match that names nothing, or a
	// pattern that matches nothing — both mean the manifest talks about a
	// surface the pinned inventories no longer contain).
	for i, r := range m.Classifications {
		if ruleHits[i] == 0 {
			problems = append(problems, "orphan classification (matches no denominator item): "+r.Match)
		}
	}

	// Honest tallies.
	counts := map[string]int{}
	for i, a := range assignment {
		_ = i
		if a >= 0 {
			counts[m.Classifications[a].Status]++
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "FAIL: "+p)
		}
		return fmt.Errorf("%d problem(s)", len(problems))
	}

	fmt.Printf("OK: %d denominator items — covered %d · not-covered %d · out-of-scope %d (quark %s · nucleus %s · orbit %s)\n",
		len(items), counts["covered"], counts["not-covered"], counts["out-of-scope"],
		pins["quark"], pins["nucleus"], pins["orbit"])
	return nil
}
