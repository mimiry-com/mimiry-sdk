// Package apispec reads the published API documents the SDK carries in spec/.
//
// The reader is for the SDK's own tests: the contract test that holds the
// library's types to the documents, and the tests that hold the library, the
// CLI and `mimiry rpc` to the documents' elevated-rights classification. One
// reader, so the three cannot disagree about what a document says. Marker,
// the text every surface shows, is the one thing the CLI uses at run time.
//
// spec/*.json are verbatim JSON bundles of the platform's public documents;
// the platform's spec validation fails when a copy drifts.
package apispec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Files are the documents the SDK implements, in spec/.
var Files = []string{"compute.json", "org.json"}

// ElevatedExtension marks an operation that needs elevated rights. Its summary
// ends with Marker; the platform checks that the two agree.
const ElevatedExtension = "x-requires-elevated-rights"

// Marker is what every public surface shows on a function that needs
// elevated rights.
const Marker = "(requires elevated rights)"

// Doc is one API document.
type Doc struct {
	Name string
	raw  map[string]any
}

// Load reads one document.
func Load(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &Doc{Name: filepath.Base(path), raw: raw}, nil
}

// LoadAll reads every document in Files from dir.
func LoadAll(dir string) ([]*Doc, error) {
	var docs []*Doc
	for _, f := range Files {
		d, err := Load(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// At follows a JSON pointer ("#/components/schemas/X/properties/y") and
// resolves what it finds.
func (d *Doc) At(ptr string) (map[string]any, error) {
	var cur any = d.raw
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s %s: %q is not an object", d.Name, ptr, part)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("%s %s: no %q", d.Name, ptr, part)
		}
		// A pointer may run on through a reference ("…/schema/properties/x"
		// where schema is a $ref), so follow one before the next step.
		if cm, ok := cur.(map[string]any); ok {
			if ref, ok := cm["$ref"].(string); ok {
				next, err := d.At(ref)
				if err != nil {
					return nil, err
				}
				cur = next
			}
		}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s %s is not an object", d.Name, ptr)
	}
	return d.Resolve(m)
}

// Resolve follows $ref, and the item schema of an array.
func (d *Doc) Resolve(m map[string]any) (map[string]any, error) {
	for i := 0; i < 10; i++ {
		if ref, ok := m["$ref"].(string); ok {
			next, err := d.At(ref)
			if err != nil {
				return nil, err
			}
			m = next
			continue
		}
		if m["type"] == "array" {
			if items, ok := m["items"].(map[string]any); ok {
				m = items
				continue
			}
		}
		return m, nil
	}
	return nil, fmt.Errorf("%s: reference chain too deep", d.Name)
}

// Properties is a schema's property names, with the members of allOf merged
// and the object branches of oneOf/anyOf (a request field that also accepts a
// shorthand, such as auto_terminate's boolean).
func (d *Doc) Properties(m map[string]any) (map[string]bool, error) {
	out := map[string]bool{}
	if props, ok := m["properties"].(map[string]any); ok {
		for k := range props {
			out[k] = true
		}
	}
	for _, combiner := range []string{"allOf", "oneOf", "anyOf"} {
		parts, _ := m[combiner].([]any)
		for _, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			r, err := d.Resolve(pm)
			if err != nil {
				return nil, err
			}
			sub, err := d.Properties(r)
			if err != nil {
				return nil, err
			}
			for k := range sub {
				out[k] = true
			}
		}
	}
	return out, nil
}

// QueryParams is an operation's query parameter names.
func (d *Doc) QueryParams(path, method string) (map[string]bool, error) {
	item, err := d.At("#/paths/" + strings.ReplaceAll(path, "/", "~1"))
	if err != nil {
		return nil, err
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s has no %s operation", d.Name, path, method)
	}
	out := map[string]bool{}
	for _, list := range []any{item["parameters"], op["parameters"]} {
		params, _ := list.([]any)
		for _, p := range params {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if pm, err = d.Resolve(pm); err != nil {
				return nil, err
			}
			if pm["in"] == "query" {
				out[pm["name"].(string)] = true
			}
		}
	}
	return out, nil
}

// Op is one operation of a document.
type Op struct {
	Doc      string
	ID       string
	Method   string // upper case
	Path     string // the template, "/api/…/{id}"
	Elevated bool
}

func (o Op) String() string { return fmt.Sprintf("%s %s (%s)", o.Method, o.Path, o.ID) }

var methods = []string{"get", "put", "post", "patch", "delete"}

// Ops is every operation in the document, in path order.
func (d *Doc) Ops() []Op {
	paths, _ := d.raw["paths"].(map[string]any)
	var out []Op
	for path, item := range paths {
		im, _ := item.(map[string]any)
		for _, m := range methods {
			op, ok := im[m].(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			elevated, _ := op[ElevatedExtension].(bool)
			out = append(out, Op{Doc: d.Name, ID: id, Method: strings.ToUpper(m), Path: path, Elevated: elevated})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Match finds the operation a request went to. Where several templates fit,
// the one with the most literal segments wins, as the API's router prefers a
// literal segment to a parameter.
func Match(docs []*Doc, method, path string) (Op, bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	best, bestLiterals, found := Op{}, -1, false
	for _, d := range docs {
		for _, op := range d.Ops() {
			if op.Method != method {
				continue
			}
			tsegs := strings.Split(strings.Trim(op.Path, "/"), "/")
			if len(tsegs) != len(segs) {
				continue
			}
			literals, ok := 0, true
			for i, t := range tsegs {
				if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
					if segs[i] == "" {
						ok = false
						break
					}
					continue
				}
				if t != segs[i] {
					ok = false
					break
				}
				literals++
			}
			if ok && literals > bestLiterals {
				best, bestLiterals, found = op, literals, true
			}
		}
	}
	return best, found
}
