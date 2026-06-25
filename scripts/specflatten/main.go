// Command specflatten reads the verbatim ubicloud OpenAPI spec and writes a
// derived copy with every allOf schema composition flattened into a single
// properties map.
//
// tfplugingen-openapi (v0.3.0, the latest release) cannot map allOf
// ("schema composition is currently not supported") and silently skips the
// response body, leaving the postgres/vm/firewall data source and resource
// schemas empty. The live spec uses allOf only in the detailed-GET response
// wrappers, each shaped as:
//
//	schema: {type: object, additionalProperties: false, allOf: [{$ref: base}, {inline}]}
//
// Flattening merges every allOf subschema's properties (resolving a
// #/components/schemas/* $ref to the referenced base) into one properties map,
// unions the required arrays, keeps additionalProperties, and drops allOf.
// oapi-codegen still reads the verbatim spec; only tfplugingen-openapi consumes
// this derived file, so the bundled spec stays a byte-for-byte upstream copy and
// drift remains a plain diff. The output is deterministic (yaml.v3 sorts map
// keys), so go generate stays diff-clean on re-run.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: specflatten <in.yml> <out.yml>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "specflatten:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}

	schemas, _ := dig(doc, "components", "schemas").(map[string]any)
	flattened := 0
	visited := map[string]bool{}
	// Flatten the named component schemas first (deterministic order) so that a wrapper
	// which $refs a base composes the base AFTER the base's own allOf is merged. Then
	// flatten the rest of the document (the response wrappers).
	for _, name := range sortedKeys(schemas) {
		flattenNamed(name, schemas, visited, &flattened)
	}
	flatten(doc, schemas, visited, &flattened)

	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, encoded, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "specflatten: flattened %d allOf schema(s) -> %s\n", flattened, out)
	return nil
}

// flattenNamed flattens components/schemas/<name> in place, marking it visited so a
// reference cycle terminates. mergeAllOf flattens any base this schema composes first.
func flattenNamed(name string, schemas map[string]any, visited map[string]bool, count *int) {
	if visited[name] {
		return
	}
	visited[name] = true
	s, ok := schemas[name].(map[string]any)
	if !ok {
		return
	}
	if subs, ok := s["allOf"].([]any); ok {
		mergeAllOf(s, subs, schemas, visited, count)
		*count++
	}
}

// flatten walks node and rewrites every object carrying an allOf into a merged
// properties/required object. Named component schemas are already flattened by the
// caller, so by the time a response wrapper here resolves a $ref the base is merged.
func flatten(node any, schemas map[string]any, visited map[string]bool, count *int) {
	switch n := node.(type) {
	case map[string]any:
		if subs, ok := n["allOf"].([]any); ok {
			mergeAllOf(n, subs, schemas, visited, count)
			*count++
		}
		for _, v := range n {
			flatten(v, schemas, visited, count)
		}
	case []any:
		for _, v := range n {
			flatten(v, schemas, visited, count)
		}
	}
}

func mergeAllOf(n map[string]any, subs []any, schemas map[string]any, visited map[string]bool, count *int) {
	props := map[string]any{}
	var required []any

	collect := func(s map[string]any) {
		if p, ok := s["properties"].(map[string]any); ok {
			for k, v := range p {
				props[k] = v
			}
		}
		if r, ok := s["required"].([]any); ok {
			required = append(required, r...)
		}
	}

	for _, sub := range subs {
		sm, ok := sub.(map[string]any)
		if !ok {
			continue
		}
		collect(resolveRef(sm, schemas, visited, count))
	}
	// Merge any properties/required already on the wrapper itself.
	collect(n)

	delete(n, "allOf")
	n["properties"] = props
	if req := dedupStrings(required); len(req) > 0 {
		n["required"] = req
	}
}

// resolveRef returns the schema referenced by a sole #/components/schemas/* $ref,
// flattening that base first so its own composition is merged before it is copied;
// returns the subschema itself when it is inline.
func resolveRef(sm map[string]any, schemas map[string]any, visited map[string]bool, count *int) map[string]any {
	ref, ok := sm["$ref"].(string)
	if !ok {
		return sm
	}
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	flattenNamed(name, schemas, visited, count)
	if target, ok := schemas[name].(map[string]any); ok {
		return target
	}
	return sm
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func dedupStrings(in []any) []any {
	seen := map[string]bool{}
	var out []any
	for _, v := range in {
		key := fmt.Sprint(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]) < fmt.Sprint(out[j])
	})
	return out
}
