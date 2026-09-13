// Package xmltree is a small, prefix-agnostic XML parse tree used by
// hapidays's document-driven importers (WSDL, OData $metadata). Both
// formats mix namespace prefixes inconsistently depending on what
// generated them (wsdl:/soap:/soap12:, or edmx:/edm:/m:, or none at all
// for the same elements), so callers match on an element's Local name —
// and only fall back to Space when telling same-named elements in
// different namespaces apart actually matters (e.g. soap:address vs
// soap12:address, or detecting EDMX version).
package xmltree

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

type Node struct {
	Local    string
	Space    string
	Attrs    map[string]string
	Children []*Node
	Text     string
}

// Parse reads r as XML into a Node tree, discarding namespace prefixes in
// favor of resolved namespace URIs (Space) plus local names (Local).
func Parse(r io.Reader) (*Node, error) {
	dec := xml.NewDecoder(r)
	var stack []*Node
	var root *Node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Local: t.Name.Local, Space: t.Name.Space, Attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.Attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no root element found")
	}
	return root, nil
}

// Children returns n's direct children named local.
func Children(n *Node, local string) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Local == local {
			out = append(out, c)
		}
	}
	return out
}

// Child returns n's first direct child named local, or nil.
func Child(n *Node, local string) *Node {
	for _, c := range n.Children {
		if c.Local == local {
			return c
		}
	}
	return nil
}

// Descendants finds every node named local anywhere under n (not just
// direct children) — needed to search a deeply/inconsistently nested
// section (WSDL's <types>, EDMX's <Schema>) without assuming a fixed depth.
func Descendants(n *Node, local string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(x *Node) {
		if x.Local == local {
			out = append(out, x)
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	for _, c := range n.Children {
		walk(c)
	}
	return out
}

// LocalName strips a namespace prefix ("tns:Foo" -> "Foo", "ODataDemo.Product"
// stays as-is since '.' isn't a QName separator) — WSDL and EDMX documents
// reference each other's elements by prefixed QName strings constantly
// (binding type=, message=, EntityType=), and the prefix is meaningless
// once callers are matching by Local name anyway.
func LocalName(qname string) string {
	if i := strings.Index(qname, ":"); i >= 0 {
		return qname[i+1:]
	}
	return qname
}
