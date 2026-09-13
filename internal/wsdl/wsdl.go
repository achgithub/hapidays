// Package wsdl turns a WSDL document into a ready-to-use Hapidays
// collection: one folder per service, one request per SOAP operation, each
// with a correctly namespaced envelope skeleton, the right SOAPAction, and
// the operation's endpoint URL already filled in.
//
// This is deliberately not a WSDL compiler. It doesn't resolve external
// <wsdl:import>/<xsd:import>, doesn't handle every binding style in the
// spec, and doesn't validate anything against XML Schema. It extracts the
// four things needed to generate a usable request (endpoint, SOAPAction,
// body wrapper element, and that element's immediate child names) and
// falls back to a placeholder comment rather than failing the whole
// import when it can't resolve something — partial success beats an
// error, since even a skeleton with the endpoint and SOAPAction right
// saves most of the manual work.
package wsdl

import (
	"fmt"
	"strings"

	"hapidays/internal/model"
	"hapidays/internal/xmltree"
)

type node = xmltree.Node

var (
	children    = xmltree.Children
	child       = xmltree.Child
	descendants = xmltree.Descendants
	localName   = xmltree.LocalName
)

func isSoap12(n *node) bool {
	return strings.Contains(n.Space, "wsdl/soap12")
}

type operation struct {
	Name        string
	Endpoint    string
	SoapVersion string // "1.1" | "1.2"
	SoapAction  string
	BodyElement string // wrapper element name; "" falls back to Name (rpc style)
	TargetNS    string
	Params      []string // child element names to emit as placeholders; nil if unresolved
}

// Import parses a WSDL document and returns a Collection with one folder
// (named after the WSDL's first <service>, or "WSDL Import") containing
// one request per discovered SOAP operation.
func Import(data []byte, sourceURL string, newID func() string) (*model.Collection, error) {
	root, err := xmltree.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse WSDL: %w", err)
	}
	if root.Local != "definitions" {
		return nil, fmt.Errorf("not a WSDL document (root element is <%s>, expected <definitions>)", root.Local)
	}
	targetNS := root.Attrs["targetNamespace"]

	ops := extractOperations(root, targetNS)
	if len(ops) == 0 {
		return nil, fmt.Errorf("no SOAP operations found in this WSDL")
	}

	serviceName := "WSDL Import"
	if svcs := children(root, "service"); len(svcs) > 0 && svcs[0].Attrs["name"] != "" {
		serviceName = svcs[0].Attrs["name"]
	}

	folder := &model.Node{ID: newID(), Name: serviceName}
	for _, op := range ops {
		folder.Children = append(folder.Children, &model.Node{
			ID:      newID(),
			Name:    op.Name,
			Request: buildRequest(op),
		})
	}

	col := &model.Collection{
		ID:   newID(),
		Name: serviceName,
		Root: []*model.Node{folder},
	}
	return col, nil
}

func extractOperations(root *node, targetNS string) []operation {
	var ops []operation

	// Map portType name -> operation name -> input message QName.
	portTypeInputs := map[string]map[string]string{}
	for _, pt := range children(root, "portType") {
		inputs := map[string]string{}
		for _, o := range children(pt, "operation") {
			if in := child(o, "input"); in != nil {
				inputs[o.Attrs["name"]] = localName(in.Attrs["message"])
			}
		}
		portTypeInputs[pt.Attrs["name"]] = inputs
	}

	// Map message name -> its body wrapper info (element ref, or rpc parts).
	type msgInfo struct {
		element  string   // document/literal: part element=
		rpcParts []string // rpc/literal: part name= for each primitive part
	}
	messages := map[string]msgInfo{}
	for _, m := range children(root, "message") {
		var info msgInfo
		for _, p := range children(m, "part") {
			if el, ok := p.Attrs["element"]; ok {
				info.element = localName(el)
			} else if p.Attrs["name"] != "" {
				info.rpcParts = append(info.rpcParts, p.Attrs["name"])
			}
		}
		messages[m.Attrs["name"]] = info
	}

	// binding name -> (portType name, soap version, per-operation soapAction)
	for _, binding := range children(root, "binding") {
		soapBinding := firstMatching(binding, "binding", func(n *node) bool {
			return strings.Contains(n.Space, "wsdl/soap")
		})
		if soapBinding == nil {
			continue // not a SOAP binding (e.g. an HTTP GET/POST binding) — skip
		}
		version := "1.1"
		if isSoap12(soapBinding) {
			version = "1.2"
		}
		portTypeName := localName(binding.Attrs["type"])
		inputs := portTypeInputs[portTypeName]

		endpoint := findEndpoint(root, binding.Attrs["name"])

		for _, opNode := range children(binding, "operation") {
			opName := opNode.Attrs["name"]
			soapAction := ""
			if soapOp := firstMatching(opNode, "operation", func(n *node) bool {
				return strings.Contains(n.Space, "wsdl/soap")
			}); soapOp != nil {
				soapAction = soapOp.Attrs["soapAction"]
			}

			op := operation{
				Name:        opName,
				Endpoint:    endpoint,
				SoapVersion: version,
				SoapAction:  soapAction,
				TargetNS:    targetNS,
			}

			if msgName, ok := inputs[opName]; ok {
				if info, ok := messages[msgName]; ok {
					if info.element != "" {
						op.BodyElement = info.element
						op.Params = resolveElementParams(root, info.element)
					} else if len(info.rpcParts) > 0 {
						op.BodyElement = opName // rpc style wraps in the operation name itself
						op.Params = info.rpcParts
					}
				}
			}
			if op.BodyElement == "" {
				op.BodyElement = opName
			}
			ops = append(ops, op)
		}
	}
	return ops
}

// firstMatching finds the first child named local for which pred holds —
// used to find a binding/operation's soap: (or soap12:) child without
// hard-coding which namespace URI it lives in.
func firstMatching(n *node, local string, pred func(*node) bool) *node {
	for _, c := range children(n, local) {
		if pred(c) {
			return c
		}
	}
	return nil
}

// findEndpoint locates the <soap:address location=…> for whichever
// <port binding="…"> references bindingName, searching every <service> —
// a WSDL can (and commonly does) declare several services.
func findEndpoint(root *node, bindingName string) string {
	for _, svc := range children(root, "service") {
		for _, port := range children(svc, "port") {
			if localName(port.Attrs["binding"]) != bindingName {
				continue
			}
			if addr := child(port, "address"); addr != nil {
				return addr.Attrs["location"]
			}
		}
	}
	return ""
}

// resolveElementParams looks up a top-level <xsd:element name=elementName>
// in <types> and returns the names of its immediate child elements (from
// an inline complexType/sequence, or from a named complexType it
// references via type=). Returns nil — not an error — if it can't resolve
// the shape; callers fall back to a placeholder comment instead.
func resolveElementParams(root *node, elementName string) []string {
	types := child(root, "types")
	if types == nil {
		return nil
	}
	var target *node
	for _, el := range descendants(types, "element") {
		if el.Attrs["name"] == elementName {
			target = el
			break
		}
	}
	if target == nil {
		return nil
	}
	if ct := child(target, "complexType"); ct != nil {
		return sequenceParamNames(ct)
	}
	if typeAttr, ok := target.Attrs["type"]; ok {
		ctName := localName(typeAttr)
		for _, ct := range descendants(types, "complexType") {
			if ct.Attrs["name"] == ctName {
				return sequenceParamNames(ct)
			}
		}
	}
	return nil
}

func sequenceParamNames(complexType *node) []string {
	seq := child(complexType, "sequence")
	if seq == nil {
		seq = child(complexType, "all") // some WSDLs use xsd:all instead of xsd:sequence
	}
	if seq == nil {
		return nil
	}
	var names []string
	for _, el := range children(seq, "element") {
		if n := el.Attrs["name"]; n != "" {
			names = append(names, n)
		}
	}
	return names
}

func buildRequest(op operation) *model.RequestSpec {
	var body strings.Builder
	if len(op.Params) > 0 {
		for _, p := range op.Params {
			body.WriteString(fmt.Sprintf("\n      <%s>?</%s>", p, p))
		}
	} else {
		body.WriteString("\n      <!-- unable to determine this operation's parameters from the WSDL — fill in by hand -->")
	}

	var envelope string
	if op.SoapVersion == "1.2" {
		envelope = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<soap12:Envelope xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:soap12="http://www.w3.org/2003/05/soap-envelope">
  <soap12:Body>
    <%s xmlns="%s">%s
    </%s>
  </soap12:Body>
</soap12:Envelope>`, op.BodyElement, op.TargetNS, body.String(), op.BodyElement)
	} else {
		envelope = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <%s xmlns="%s">%s
    </%s>
  </soap:Body>
</soap:Envelope>`, op.BodyElement, op.TargetNS, body.String(), op.BodyElement)
	}

	return &model.RequestSpec{
		Method: "POST",
		URLRaw: op.Endpoint,
		Auth:   model.Auth{Type: model.AuthInherit},
		Body: model.Body{
			Mode:        model.BodySoap,
			Raw:         envelope,
			RawLanguage: "xml",
			SoapVersion: op.SoapVersion,
			SoapAction:  op.SoapAction,
		},
	}
}
