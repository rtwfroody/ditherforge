// Package materialx parses and evaluates a procedural subset of the
// MaterialX (.mtlx) format, sampling shader graphs at 3D positions to
// produce surface colors. Only the nodes needed to evaluate solid
// procedural patterns (marble, brick, checkerboard, etc.) are
// implemented; image-based, BSDF, and lighting nodes are intentionally
// out of scope — the consumer is ditherforge's voxel-color pipeline,
// which only needs RGB at a 3D point.
package materialx

import (
	"fmt"
	"strconv"
	"strings"
)

type ValueType int

const (
	TypeUnknown ValueType = iota
	TypeFloat
	TypeInteger
	TypeVector2
	TypeVector3
	TypeVector4
	TypeColor3
	TypeColor4
)

func (t ValueType) String() string {
	switch t {
	case TypeFloat:
		return "float"
	case TypeInteger:
		return "integer"
	case TypeVector2:
		return "vector2"
	case TypeVector3:
		return "vector3"
	case TypeVector4:
		return "vector4"
	case TypeColor3:
		return "color3"
	case TypeColor4:
		return "color4"
	}
	return "unknown"
}

func parseValueType(s string) ValueType {
	switch strings.TrimSpace(s) {
	case "float":
		return TypeFloat
	case "integer":
		return TypeInteger
	case "vector2":
		return TypeVector2
	case "vector3":
		return TypeVector3
	case "vector4":
		return TypeVector4
	case "color3":
		return TypeColor3
	case "color4":
		return TypeColor4
	}
	return TypeUnknown
}

// Value is a tagged union over MaterialX scalar/vector/color types. Vec
// holds 2/3/4 components for vectors and colors; F/I hold scalars.
type Value struct {
	Type ValueType
	F    float64
	I    int
	Vec  [4]float64
}

func FloatValue(f float64) Value             { return Value{Type: TypeFloat, F: f} }
func IntValue(i int) Value                   { return Value{Type: TypeInteger, I: i} }
func Vec3Value(v [3]float64) Value           { return Value{Type: TypeVector3, Vec: [4]float64{v[0], v[1], v[2], 0}} }
func Color3Value(v [3]float64) Value         { return Value{Type: TypeColor3, Vec: [4]float64{v[0], v[1], v[2], 0}} }

func (v Value) AsFloat() float64 {
	switch v.Type {
	case TypeFloat:
		return v.F
	case TypeInteger:
		return float64(v.I)
	case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
		return v.Vec[0]
	}
	return 0
}

func (v Value) AsInt() int {
	switch v.Type {
	case TypeInteger:
		return v.I
	case TypeFloat:
		return int(v.F)
	}
	return 0
}

func (v Value) AsVec3() [3]float64 {
	switch v.Type {
	case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
		return [3]float64{v.Vec[0], v.Vec[1], v.Vec[2]}
	case TypeFloat:
		return [3]float64{v.F, v.F, v.F}
	case TypeInteger:
		f := float64(v.I)
		return [3]float64{f, f, f}
	}
	return [3]float64{}
}

// parseValueString converts a MaterialX attribute string ("0.8, 0.8, 0.8",
// "3.0", "3", "1, 1, 1") into a typed Value.
func parseValueString(s string, typ ValueType) (Value, error) {
	s = strings.TrimSpace(s)
	switch typ {
	case TypeFloat:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return Value{}, err
		}
		return FloatValue(f), nil
	case TypeInteger:
		i, err := strconv.Atoi(s)
		if err != nil {
			return Value{}, err
		}
		return IntValue(i), nil
	case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
		parts := strings.Split(s, ",")
		need := vecArity(typ)
		if len(parts) != need {
			return Value{}, fmt.Errorf("type %s expects %d components, got %d", typ, need, len(parts))
		}
		v := Value{Type: typ}
		for i, p := range parts {
			f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return Value{}, fmt.Errorf("component %d: %w", i, err)
			}
			v.Vec[i] = f
		}
		return v, nil
	}
	return Value{}, fmt.Errorf("unsupported value type %s", typ)
}

func vecArity(t ValueType) int {
	switch t {
	case TypeVector2:
		return 2
	case TypeVector3, TypeColor3:
		return 3
	case TypeVector4, TypeColor4:
		return 4
	}
	return 0
}

// Document is the parsed contents of a .mtlx file.
type Document struct {
	NodeGraphs map[string]*nodeGraph
	Surfaces   map[string]*surface
	Materials  map[string]*material
}

type nodeGraph struct {
	Name          string
	Inputs        []*graphInput
	Nodes         []*node
	Outputs       []*graphOutput
	inputsByName  map[string]*graphInput
	nodesByName   map[string]*node
	outputsByName map[string]*graphOutput
}

type graphInput struct {
	Name    string
	Type    ValueType
	Default Value
}

type graphOutput struct {
	Name       string
	Type       ValueType
	NodeName   string
	OutputName string
}

type node struct {
	Type         string
	Name         string
	OutputType   ValueType
	Inputs       []*input
	inputsByName map[string]*input
}

type input struct {
	Name          string
	Type          ValueType
	Value         *Value
	NodeName      string
	InterfaceName string
	GraphName     string
	OutputName    string
}

type surface struct {
	Name       string
	ShaderType string // e.g. "standard_surface", "open_pbr_surface"
	Inputs     map[string]*input
}

type material struct {
	Name              string
	SurfaceShaderName string
}
