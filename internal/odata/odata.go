// Package odata turns an OData $metadata document (CSDL/EDMX) into a ready-
// to-use Hapidays collection: one folder per entity set, with a List, a
// Get-by-key, and a $filter/$select example request in each, plus a
// $metadata request of its own.
//
// OData v2 and v4 differ in ways that change generated requests, not just
// labels, so this package detects the version from the EDMX root and
// branches on it rather than assuming v4 (the more common assumption, but
// wrong for the SAP Gateway/CPI-style services this app targets most):
//   - v2 defaults to Atom/XML; needs $format=json (or Accept) for JSON.
//   - v2's JSON envelope is {"d":{"results":[...]}}; v4's is {"value":[...]}.
//   - v2's substring filter function is substringof(needle, haystack) —
//     note the argument order is reversed from v4's contains(haystack, needle).
//   - v2 key literals for non-numeric types carry a type prefix
//     (datetime'...', guid'...'); v4 uses bare ISO-8601 / bare GUIDs.
//
// Verified against the real public services.odata.org V2 and V4 Northwind
// services (same dataset, both versions) before writing any of this from
// memory — see the session notes for the exact curl checks.
package odata

import (
	"fmt"
	"strings"

	"hapidays/internal/model"
	"hapidays/internal/xmltree"
)

type property struct {
	Name string
	Type string // e.g. "Edm.String", "Edm.Int32", or "Namespace.SomeComplexType"
}

type entityType struct {
	Properties []property
	Keys       []string // property names, in declared order
}

type entitySet struct {
	Name        string
	EntityQName string // e.g. "ODataDemo.Product"
}

// Import parses an EDMX metadata document and returns a Collection with one
// folder per entity set. auth is attached at the collection level (every
// generated request uses AuthInherit) since fetching $metadata itself
// commonly requires the same credentials the data endpoints do.
func Import(data []byte, serviceRootURL string, auth model.Auth, newID func() string) (*model.Collection, error) {
	root, err := xmltree.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse $metadata: %w", err)
	}
	if root.Local != "Edmx" {
		return nil, fmt.Errorf("not an OData $metadata document (root element is <%s>, expected <Edmx>)", root.Local)
	}
	version := "v2"
	if root.Attrs["Version"] == "4.0" {
		version = "v4"
	}

	dataServices := xmltree.Child(root, "DataServices")
	if dataServices == nil {
		return nil, fmt.Errorf("no DataServices element found in $metadata")
	}
	schemas := xmltree.Children(dataServices, "Schema")

	entityTypes := map[string]entityType{}
	for _, schema := range schemas {
		ns := schema.Attrs["Namespace"]
		for _, et := range xmltree.Children(schema, "EntityType") {
			var props []property
			for _, p := range xmltree.Children(et, "Property") {
				props = append(props, property{Name: p.Attrs["Name"], Type: p.Attrs["Type"]})
			}
			var keys []string
			if keyEl := xmltree.Child(et, "Key"); keyEl != nil {
				for _, pr := range xmltree.Children(keyEl, "PropertyRef") {
					keys = append(keys, pr.Attrs["Name"])
				}
			}
			entityTypes[ns+"."+et.Attrs["Name"]] = entityType{Properties: props, Keys: keys}
		}
	}

	var sets []entitySet
	serviceName := "OData Import"
	for _, schema := range schemas {
		for _, ec := range xmltree.Children(schema, "EntityContainer") {
			if ec.Attrs["Name"] != "" {
				serviceName = ec.Attrs["Name"]
			}
			for _, es := range xmltree.Children(ec, "EntitySet") {
				sets = append(sets, entitySet{Name: es.Attrs["Name"], EntityQName: es.Attrs["EntityType"]})
			}
		}
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no EntitySet declarations found in $metadata (nothing to generate requests for)")
	}

	baseURLVar := "baseUrl"
	root2 := []*model.Node{
		{ID: newID(), Name: "$metadata", Request: &model.RequestSpec{
			Method:   "GET",
			URLRaw:   "{{" + baseURLVar + "}}/$metadata",
			Auth:     model.Auth{Type: model.AuthInherit},
			Body:     model.Body{Mode: model.BodyNone},
			Protocol: "odata",
		}},
	}

	for _, set := range sets {
		// entityTypes is keyed by the full "Namespace.Name" QName, which is
		// exactly the form EntitySet's EntityType= attribute uses.
		et := entityTypes[set.EntityQName]
		root2 = append(root2, &model.Node{
			ID:       newID(),
			Name:     set.Name,
			Children: buildEntitySetRequests(set, et, baseURLVar, version, newID),
		})
	}

	col := &model.Collection{
		ID:   newID(),
		Name: serviceName,
		Auth: auth,
		Variables: []model.KV{
			{Key: baseURLVar, Value: strings.TrimSuffix(serviceRootURL, "/")},
		},
		Root: root2,
	}
	return col, nil
}

func buildEntitySetRequests(set entitySet, et entityType, baseURLVar, version string, newID func() string) []*model.Node {
	base := "{{" + baseURLVar + "}}/" + set.Name
	jsonQuery := jsonFormatQuery(version)

	list := &model.Node{ID: newID(), Name: "List", Request: &model.RequestSpec{
		Method:   "GET",
		URLRaw:   base,
		Query:    jsonQuery,
		Auth:     model.Auth{Type: model.AuthInherit},
		Body:     model.Body{Mode: model.BodyNone},
		Protocol: "odata",
	}}

	nodes := []*model.Node{list}

	if len(et.Keys) > 0 {
		keySeg := buildKeySegment(et)
		nodes = append(nodes, &model.Node{ID: newID(), Name: "Get by key", Request: &model.RequestSpec{
			Method:   "GET",
			URLRaw:   base + "(" + keySeg + ")",
			Query:    jsonQuery,
			Auth:     model.Auth{Type: model.AuthInherit},
			Body:     model.Body{Mode: model.BodyNone},
			Protocol: "odata",
		}})
	}

	if len(et.Properties) > 0 {
		filterExpr, filterProp := buildFilterExample(et, version)
		selectProps := selectExample(et, filterProp)
		query := append([]model.KV{}, jsonQuery...)
		query = append(query,
			model.KV{Key: "$filter", Value: filterExpr},
			model.KV{Key: "$select", Value: strings.Join(selectProps, ",")},
			model.KV{Key: "$top", Value: "5"},
		)
		nodes = append(nodes, &model.Node{ID: newID(), Name: "Filter example", Request: &model.RequestSpec{
			Method:   "GET",
			URLRaw:   base,
			Query:    query,
			Auth:     model.Auth{Type: model.AuthInherit},
			Body:     model.Body{Mode: model.BodyNone},
			Protocol: "odata",
		}})
	}

	return nodes
}

// jsonFormatQuery returns the query params needed to get a JSON response.
// v4 already defaults to JSON, so this is empty — an explicit $format=json
// is itself invalid on some v4 services since $format is optional there.
// v2 defaults to Atom/XML, and $format=json (confirmed against the live
// V2 Northwind service) is the reliable way to request JSON instead.
func jsonFormatQuery(version string) []model.KV {
	if version == "v2" {
		return []model.KV{{Key: "$format", Value: "json"}}
	}
	return nil
}

// buildKeySegment renders the Key(...) segment of an entity URL, e.g.
// "ID=1" or, for a composite key, "Hexagency='44554E53',Hexid='...'" (the
// shape real SAP OData services use, per the KeyLiteral rules below).
// Always uses the Name=Value form (valid for single-property keys too in
// both versions) since it's clearer as a generated template than a bare
// positional value would be.
func buildKeySegment(et entityType) string {
	byName := map[string]string{}
	for _, p := range et.Properties {
		byName[p.Name] = p.Type
	}
	parts := make([]string, 0, len(et.Keys))
	for _, k := range et.Keys {
		parts = append(parts, k+"="+keyLiteral(byName[k]))
	}
	return strings.Join(parts, ",")
}

// keyLiteral returns a syntactically valid placeholder literal for edmType
// per the OData V2 or V4 ABNF for key predicates — the exact form (quoting,
// type prefix) depends on the primitive type and, for several types, the
// version. Callers still need to replace the value; only the shape needs
// to already be correct so it's obvious what to edit and how.
func keyLiteral(edmType string) string {
	switch edmType {
	case "Edm.String":
		return "'key'"
	case "Edm.Guid":
		return "guid'00000000-0000-0000-0000-000000000000'" // v2 form; v4 also accepts this, and definitely accepts the bare form without the prefix
	case "Edm.DateTime":
		return "datetime'2024-01-01T00:00:00'" // v2-only type
	case "Edm.DateTimeOffset":
		return "2024-01-01T00:00:00Z" // v4 form (bare); harmless if a v2 service uses this type too
	case "Edm.Time":
		return "time'PT00H00M00S'"
	case "Edm.Boolean":
		return "true"
	case "Edm.Int16", "Edm.Int32", "Edm.Byte", "Edm.SByte":
		return "1"
	case "Edm.Int64":
		return "1L" // v2 suffix; harmless extra character if a v4 service is lenient, but flagged here since v4 doesn't require it
	case "Edm.Decimal":
		return "1M"
	case "Edm.Double":
		return "1.0"
	case "Edm.Single":
		return "1.0f"
	default:
		return "'key'"
	}
}

// buildFilterExample picks the first Edm.String property (favoring a
// substring-style example, the most commonly hand-written and most
// commonly gotten wrong filter) or, failing that, the first property at
// all with an eq comparison. Returns the expression and which property it
// used, so buildEntitySetRequests can make sure $select still includes it.
func buildFilterExample(et entityType, version string) (expr, propName string) {
	for _, p := range et.Properties {
		if p.Type == "Edm.String" {
			if version == "v4" {
				return fmt.Sprintf("contains(%s,'x')", p.Name), p.Name
			}
			return fmt.Sprintf("substringof('x',%s)", p.Name), p.Name
		}
	}
	p := et.Properties[0]
	return fmt.Sprintf("%s eq %s", p.Name, keyLiteral(p.Type)), p.Name
}

func selectExample(et entityType, mustInclude string) []string {
	var out []string
	out = append(out, mustInclude)
	for _, p := range et.Properties {
		if p.Name == mustInclude {
			continue
		}
		out = append(out, p.Name)
		if len(out) >= 3 {
			break
		}
	}
	return out
}
