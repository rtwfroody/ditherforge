package materialx

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
)

// Parse reads a MaterialX document from r.
func Parse(r io.Reader) (*Document, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, errors.New("materialx: no <materialx> root element")
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local != "materialx" {
			return nil, fmt.Errorf("materialx: expected root <materialx>, got <%s>", se.Name.Local)
		}
		return parseMaterialX(dec)
	}
}

// ParseFile is a convenience wrapper around Parse.
func ParseFile(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// ParseBytes parses MaterialX from an in-memory byte slice.
func ParseBytes(b []byte) (*Document, error) {
	return Parse(bytes.NewReader(b))
}

func parseMaterialX(dec *xml.Decoder) (*Document, error) {
	doc := &Document{
		NodeGraphs: map[string]*nodeGraph{},
		Surfaces:   map[string]*surface{},
		Materials:  map[string]*material{},
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "nodegraph":
				ng, err := parseNodeGraph(dec, t)
				if err != nil {
					return nil, err
				}
				doc.NodeGraphs[ng.Name] = ng
			case "surfacematerial":
				m, err := parseMaterial(dec, t)
				if err != nil {
					return nil, err
				}
				doc.Materials[m.Name] = m
			default:
				// Surface shaders are dispatched by their type="surfaceshader"
				// attribute rather than element name, so the parser supports
				// standard_surface, open_pbr_surface, UsdPreviewSurface, etc.
				// without an element-name allowlist.
				if isSurfaceShader(t) {
					s, err := parseSurfaceShader(dec, t)
					if err != nil {
						return nil, err
					}
					doc.Surfaces[s.Name] = s
				} else if err := skipElement(dec); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			return doc, nil
		}
	}
}

func isSurfaceShader(se xml.StartElement) bool {
	for _, a := range se.Attr {
		if a.Name.Local == "type" && a.Value == "surfaceshader" {
			return true
		}
	}
	return false
}

func parseNodeGraph(dec *xml.Decoder, se xml.StartElement) (*nodeGraph, error) {
	ng := &nodeGraph{
		inputsByName:  map[string]*graphInput{},
		nodesByName:   map[string]*node{},
		outputsByName: map[string]*graphOutput{},
	}
	for _, a := range se.Attr {
		if a.Name.Local == "name" {
			ng.Name = a.Value
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "input":
				gi, err := parseGraphInput(dec, t)
				if err != nil {
					return nil, fmt.Errorf("nodegraph %q: %w", ng.Name, err)
				}
				ng.Inputs = append(ng.Inputs, gi)
				ng.inputsByName[gi.Name] = gi
			case "output":
				gout, err := parseGraphOutput(dec, t)
				if err != nil {
					return nil, fmt.Errorf("nodegraph %q: %w", ng.Name, err)
				}
				ng.Outputs = append(ng.Outputs, gout)
				ng.outputsByName[gout.Name] = gout
			default:
				n, err := parseNode(dec, t)
				if err != nil {
					return nil, fmt.Errorf("nodegraph %q: %w", ng.Name, err)
				}
				ng.Nodes = append(ng.Nodes, n)
				ng.nodesByName[n.Name] = n
			}
		case xml.EndElement:
			return ng, nil
		}
	}
}

// attrLookup returns the value of the named attribute and whether it
// was present. XML attributes are unordered, so any code that depends
// on multiple attributes (e.g. parsing a value against its declared
// type) must collect them up-front instead of consuming them in
// iteration order.
func attrLookup(se xml.StartElement, name string) (string, bool) {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

func parseGraphInput(dec *xml.Decoder, se xml.StartElement) (*graphInput, error) {
	gi := &graphInput{}
	if v, ok := attrLookup(se, "name"); ok {
		gi.Name = v
	}
	if v, ok := attrLookup(se, "type"); ok {
		gi.Type = parseValueType(v)
	}
	if valueStr, ok := attrLookup(se, "value"); ok {
		v, err := parseValueString(valueStr, gi.Type)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", gi.Name, err)
		}
		gi.Default = v
	}
	if err := skipElement(dec); err != nil {
		return nil, err
	}
	return gi, nil
}

func parseGraphOutput(dec *xml.Decoder, se xml.StartElement) (*graphOutput, error) {
	o := &graphOutput{}
	if v, ok := attrLookup(se, "name"); ok {
		o.Name = v
	}
	if v, ok := attrLookup(se, "type"); ok {
		o.Type = parseValueType(v)
	}
	if v, ok := attrLookup(se, "nodename"); ok {
		o.NodeName = v
	}
	if v, ok := attrLookup(se, "output"); ok {
		o.OutputName = v
	}
	if err := skipElement(dec); err != nil {
		return nil, err
	}
	return o, nil
}

func parseNode(dec *xml.Decoder, se xml.StartElement) (*node, error) {
	n := &node{
		Type:         se.Name.Local,
		inputsByName: map[string]*input{},
	}
	if v, ok := attrLookup(se, "name"); ok {
		n.Name = v
	}
	if v, ok := attrLookup(se, "type"); ok {
		n.OutputType = parseValueType(v)
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "input" {
				in, err := parseInput(dec, t)
				if err != nil {
					return nil, fmt.Errorf("node %q: %w", n.Name, err)
				}
				n.Inputs = append(n.Inputs, in)
				n.inputsByName[in.Name] = in
			} else if err := skipElement(dec); err != nil {
				return nil, err
			}
		case xml.EndElement:
			return n, nil
		}
	}
}

func parseInput(dec *xml.Decoder, se xml.StartElement) (*input, error) {
	in := &input{}
	if v, ok := attrLookup(se, "name"); ok {
		in.Name = v
	}
	if v, ok := attrLookup(se, "type"); ok {
		in.Type = parseValueType(v)
	}
	if v, ok := attrLookup(se, "nodename"); ok {
		in.NodeName = v
	}
	if v, ok := attrLookup(se, "interfacename"); ok {
		in.InterfaceName = v
	}
	if v, ok := attrLookup(se, "nodegraph"); ok {
		in.GraphName = v
	}
	if v, ok := attrLookup(se, "output"); ok {
		in.OutputName = v
	}
	if valueStr, ok := attrLookup(se, "value"); ok {
		v, err := parseValueString(valueStr, in.Type)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", in.Name, err)
		}
		in.Value = &v
	}
	if err := skipElement(dec); err != nil {
		return nil, err
	}
	return in, nil
}

func parseSurfaceShader(dec *xml.Decoder, se xml.StartElement) (*surface, error) {
	s := &surface{
		ShaderType: se.Name.Local,
		Inputs:     map[string]*input{},
	}
	if v, ok := attrLookup(se, "name"); ok {
		s.Name = v
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "input" {
				in, err := parseInput(dec, t)
				if err != nil {
					return nil, fmt.Errorf("surface %q: %w", s.Name, err)
				}
				s.Inputs[in.Name] = in
			} else if err := skipElement(dec); err != nil {
				return nil, err
			}
		case xml.EndElement:
			return s, nil
		}
	}
}

func parseMaterial(dec *xml.Decoder, se xml.StartElement) (*material, error) {
	m := &material{}
	if v, ok := attrLookup(se, "name"); ok {
		m.Name = v
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "input" {
				in, err := parseInput(dec, t)
				if err != nil {
					return nil, err
				}
				if in.Name == "surfaceshader" {
					m.SurfaceShaderName = in.NodeName
				}
			} else if err := skipElement(dec); err != nil {
				return nil, err
			}
		case xml.EndElement:
			return m, nil
		}
	}
}

// skipElement consumes the remainder of the current element through its
// matching EndElement (handling nested children).
func skipElement(dec *xml.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}
