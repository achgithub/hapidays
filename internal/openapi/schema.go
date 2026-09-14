package openapi

import (
	"sort"
	"strings"
)

// resolvePointer follows a JSON Pointer ($ref value, e.g.
// "#/components/schemas/Pet" or "#/definitions/Pet") against root. Works
// for both spec versions unchanged — the pointer already carries its own
// full path, so there's no need to know which one produced it.
func resolvePointer(root map[string]any, ref string) (map[string]any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false // external-file $refs aren't fetched — out of scope, same as everywhere else here
	}
	var cur any = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part) // JSON Pointer escaping (RFC 6901)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

// schemaPlaceholder generates a JSON-serializable example value from a
// (possibly $ref'd) JSON Schema fragment — the same problem
// graphqlintro.argPlaceholder solves for GraphQL input types, same
// approach: required-fields-only for objects, example/default/enum-aware,
// and a cycle guard for a self-referential schema (e.g. a Category schema
// nesting itself, or a linked-list-shaped one) using the same
// already-visiting-this-$ref technique argPlaceholder's visiting set uses.
//
// Scope, same as every other importer's doc comment: type, properties,
// items, required, enum, example/default, $ref, and allOf (merged). Does
// NOT attempt oneOf/anyOf (too ambiguous to guess which alternative) or
// validation-only keywords (pattern, minimum, etc. — irrelevant to
// generating an example).
func schemaPlaceholder(schema map[string]any, root map[string]any, visiting map[string]bool) any {
	if schema == nil {
		return nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		if visiting[ref] {
			return map[string]any{} // self-referential — stop rather than recurse forever
		}
		resolved, ok := resolvePointer(root, ref)
		if !ok {
			return map[string]any{}
		}
		visiting[ref] = true
		defer delete(visiting, ref)
		return schemaPlaceholder(resolved, root, visiting)
	}
	if allOf, ok := schema["allOf"].([]any); ok && len(allOf) > 0 {
		return schemaPlaceholder(mergeAllOf(allOf, root), root, visiting)
	}
	if ex, ok := schema["example"]; ok {
		return ex
	}
	if def, ok := schema["default"]; ok {
		return def
	}
	if enumVals, ok := schema["enum"].([]any); ok && len(enumVals) > 0 {
		return enumVals[0]
	}

	typ, _ := schema["type"].(string)
	switch typ {
	case "string":
		return stringPlaceholder(schema)
	case "integer", "number":
		return 0
	case "boolean":
		return true
	case "array":
		items, _ := schema["items"].(map[string]any)
		return []any{schemaPlaceholder(items, root, visiting)}
	case "object", "": // no "type" at all is common shorthand for an object schema
		props, _ := schema["properties"].(map[string]any)
		if len(props) == 0 {
			return map[string]any{}
		}
		required := map[string]bool{}
		if reqList, ok := schema["required"].([]any); ok {
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		out := map[string]any{}
		for name, propSchema := range props {
			if !required[name] {
				continue // optional — leaving it out is valid and keeps the placeholder smaller
			}
			ps, _ := propSchema.(map[string]any)
			out[name] = schemaPlaceholder(ps, root, visiting)
		}
		return out
	default:
		return "?"
	}
}

// mergeAllOf flattens allOf's branches (each possibly its own $ref) into
// one synthetic object schema — the common real-world use of allOf is
// "base schema + this operation's extra fields," which merging into a
// single properties/required set handles correctly; allOf branches that
// aren't object schemas (rare) contribute nothing, same as an unresolvable
// $ref would.
func mergeAllOf(allOf []any, root map[string]any) map[string]any {
	props := map[string]any{}
	var required []any
	for _, branch := range allOf {
		bm, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := bm["$ref"].(string); ok {
			if resolved, ok := resolvePointer(root, ref); ok {
				bm = resolved
			}
		}
		if p, ok := bm["properties"].(map[string]any); ok {
			for k, v := range p {
				props[k] = v
			}
		}
		if r, ok := bm["required"].([]any); ok {
			required = append(required, r...)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func stringPlaceholder(schema map[string]any) string {
	format, _ := schema["format"].(string)
	switch format {
	case "date-time":
		return "2024-01-01T00:00:00Z"
	case "date":
		return "2024-01-01"
	case "email":
		return "user@example.com"
	case "uuid":
		return "00000000-0000-0000-0000-000000000000"
	case "uri", "url", "hostname":
		return "https://example.com"
	default:
		return "?"
	}
}

// sortedKeys is a small shared helper — several places (path map, tag
// grouping) need deterministic iteration order, since Go's map iteration
// deliberately isn't stable and this importer's output should be the same
// every time it's run against the same spec.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
