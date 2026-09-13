// Package graphqlintro turns a GraphQL schema (fetched via the standard
// introspection query — GraphQL's equivalent of a WSDL or an OData
// $metadata document) into a ready-to-use Hapidays collection: one request
// per top-level query/mutation field, with a placeholder-filled argument
// list and a one-level selection set of the return type's scalar/enum
// fields.
//
// Deliberately shallow, same reasoning as the WSDL and OData importers: it
// doesn't recursively resolve nested object-typed fields (that selection
// set would need its own sub-selection, and so on down an arbitrarily deep
// graph) or fully resolve nested INPUT_OBJECT arguments beyond one level.
// A one-level query that already parses and only needs its placeholder
// values replaced covers most of the manual work; deeper resolution is
// something a user extends by hand from a real, working starting point.
package graphqlintro

import (
	"encoding/json"
	"fmt"
	"strings"

	"hapidays/internal/model"
)

// IntrospectionQuery is the standard GraphQL introspection query (the same
// shape graphql-js/graphiql/most tooling sends) — deep enough (6 levels of
// ofType) to unwrap any realistic combination of NON_NULL/LIST wrapping
// down to the named type, since GraphQL introspection has no way to
// express a recursive fragment.
const IntrospectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    types {
      kind
      name
      fields(includeDeprecated: true) {
        name
        args { ...InputValue }
        type { ...TypeRef }
      }
      inputFields { ...InputValue }
      enumValues(includeDeprecated: true) { name }
    }
  }
}
fragment InputValue on __InputValue {
  name
  type { ...TypeRef }
}
fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
            }
          }
        }
      }
    }
  }
}`

type typeWrap struct {
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	OfType *typeWrap `json:"ofType"`
}

// underlying peels through NON_NULL/LIST wrappers to the named type
// underneath (e.g. "[String!]!" -> ("SCALAR", "String")).
func (t *typeWrap) underlying() (kind, name string) {
	for cur := t; cur != nil; cur = cur.OfType {
		if cur.Name != "" {
			return cur.Kind, cur.Name
		}
	}
	return "", ""
}

// isNonNull reports whether the outermost wrapper is NON_NULL — used to
// decide which input object fields are worth including in a generated
// placeholder (an optional field left out is still valid; a required one
// left out makes the generated query outright fail).
func (t *typeWrap) isNonNull() bool {
	return t != nil && t.Kind == "NON_NULL"
}

type inputValue struct {
	Name string   `json:"name"`
	Type typeWrap `json:"type"`
}

type field struct {
	Name string       `json:"name"`
	Args []inputValue `json:"args"`
	Type typeWrap     `json:"type"`
}

type enumValue struct {
	Name string `json:"name"`
}

type fullType struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Fields      []field      `json:"fields"`
	InputFields []inputValue `json:"inputFields"`
	EnumValues  []enumValue  `json:"enumValues"`
}

type introspectionResponse struct {
	Data struct {
		Schema struct {
			QueryType    *struct{ Name string } `json:"queryType"`
			MutationType *struct{ Name string } `json:"mutationType"`
			Types        []fullType             `json:"types"`
		} `json:"__schema"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Import parses the response body of an introspection query (as returned
// by POSTing IntrospectionQuery to a GraphQL endpoint) and returns a
// Collection with a "Queries" folder and, if the schema has one, a
// "Mutations" folder — one request per top-level field.
func Import(data []byte, endpointURL string, auth model.Auth, newID func() string) (*model.Collection, error) {
	var resp introspectionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse introspection response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL server returned an error for the introspection query: %s", resp.Errors[0].Message)
	}
	if resp.Data.Schema.QueryType == nil {
		return nil, fmt.Errorf("introspection response has no __schema.queryType — is this a GraphQL endpoint?")
	}

	byName := map[string]fullType{}
	for _, t := range resp.Data.Schema.Types {
		byName[t.Name] = t
	}

	var root []*model.Node
	if q, ok := byName[resp.Data.Schema.QueryType.Name]; ok {
		root = append(root, buildOperationFolder("Queries", "query", q, byName, endpointURL, auth, newID))
	}
	if resp.Data.Schema.MutationType != nil {
		if m, ok := byName[resp.Data.Schema.MutationType.Name]; ok {
			root = append(root, buildOperationFolder("Mutations", "mutation", m, byName, endpointURL, auth, newID))
		}
	}
	if len(root) == 0 {
		return nil, fmt.Errorf("no queryable/mutable fields found in this schema")
	}

	return &model.Collection{
		ID:   newID(),
		Name: "GraphQL Import",
		Auth: auth,
		Root: root,
	}, nil
}

func buildOperationFolder(folderName, opKeyword string, opType fullType, byName map[string]fullType, endpointURL string, auth model.Auth, newID func() string) *model.Node {
	folder := &model.Node{ID: newID(), Name: folderName}
	for _, f := range opType.Fields {
		folder.Children = append(folder.Children, &model.Node{
			ID:      newID(),
			Name:    f.Name,
			Request: buildRequest(opKeyword, f, byName, endpointURL, auth),
		})
	}
	return folder
}

func buildRequest(opKeyword string, f field, byName map[string]fullType, endpointURL string, auth model.Auth) *model.RequestSpec {
	var b strings.Builder
	fmt.Fprintf(&b, "%s {\n  %s", opKeyword, f.Name)

	if len(f.Args) > 0 {
		var args []string
		for _, a := range f.Args {
			args = append(args, a.Name+": "+argPlaceholder(a.Type, byName, 0))
		}
		fmt.Fprintf(&b, "(%s)", strings.Join(args, ", "))
	}

	if sel := selectionSet(f.Type, byName, 0); sel != "" {
		fmt.Fprintf(&b, " {\n%s\n  }", sel)
	}
	b.WriteString("\n}")

	payload, _ := json.Marshal(map[string]string{"query": b.String()})

	return &model.RequestSpec{
		Method: "POST",
		URLRaw: endpointURL,
		Auth:   model.Auth{Type: model.AuthInherit},
		Body: model.Body{
			Mode:        model.BodyGraphQL,
			Raw:         string(payload),
			RawLanguage: "json",
		},
	}
}

// selectionSet returns the "{ field1\n field2 }" body (without the outer
// braces) for objType's scalar/enum fields, one level deep. depth guards
// against ever recursing further even if called from a context that
// would try to (there currently isn't one — everything here calls with
// depth 0 — but a guard costs nothing and this is exactly the kind of
// thing that grows a recursive call later without anyone re-checking the
// termination condition).
func selectionSet(t typeWrap, byName map[string]fullType, depth int) string {
	if depth > 0 {
		return ""
	}
	kind, name := t.underlying()
	if kind != "OBJECT" && kind != "INTERFACE" {
		return "" // scalar/enum leaf — no selection set needed (or allowed)
	}
	target, ok := byName[name]
	if !ok {
		return ""
	}
	var lines []string
	for _, sf := range target.Fields {
		subKind, _ := sf.Type.underlying()
		if subKind == "SCALAR" || subKind == "ENUM" {
			lines = append(lines, "    "+sf.Name)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, "    __typename") // every OBJECT/INTERFACE has this; keeps the selection set non-empty
	}
	return strings.Join(lines, "\n")
}

// argPlaceholder renders a syntactically valid GraphQL literal for an
// argument's type: a scalar gets a type-appropriate placeholder, an enum
// gets one of its real member names (bare, unquoted — GraphQL enum
// literals aren't strings), a list wraps the single-item placeholder in
// brackets, and an INPUT_OBJECT gets its required (NON_NULL) fields filled
// in one level deep — enough that the generated query has a shot at
// actually validating instead of being rejected for a missing required
// field, without trying to fully resolve arbitrarily nested input shapes.
func argPlaceholder(t typeWrap, byName map[string]fullType, depth int) string {
	if t.Kind == "LIST" && t.OfType != nil {
		return "[" + argPlaceholder(*t.OfType, byName, depth) + "]"
	}
	if t.Kind == "NON_NULL" && t.OfType != nil {
		return argPlaceholder(*t.OfType, byName, depth)
	}
	kind, name := t.underlying()
	switch kind {
	case "SCALAR":
		return scalarPlaceholder(name)
	case "ENUM":
		if et, ok := byName[name]; ok && len(et.EnumValues) > 0 {
			return et.EnumValues[0].Name
		}
		return "UNKNOWN_ENUM_VALUE"
	case "INPUT_OBJECT":
		if depth >= 1 {
			return "{}" // don't recurse past one level of input object nesting
		}
		it, ok := byName[name]
		if !ok {
			return "{}"
		}
		var fields []string
		for _, inf := range it.InputFields {
			if !inf.Type.isNonNull() {
				continue // optional — leaving it out is valid and keeps the placeholder smaller
			}
			fields = append(fields, inf.Name+": "+argPlaceholder(inf.Type, byName, depth+1))
		}
		if len(fields) == 0 {
			return "{}"
		}
		return "{ " + strings.Join(fields, ", ") + " }"
	default:
		return `"?"`
	}
}

func scalarPlaceholder(name string) string {
	switch name {
	case "Int", "Float":
		return "0"
	case "Boolean":
		return "true"
	case "ID", "String":
		return `"?"`
	default:
		return `"?"` // custom scalars (DateTime, JSON, etc.) — string is the safest generic guess
	}
}
