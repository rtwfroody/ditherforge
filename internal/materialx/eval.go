package materialx

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Sampler returns an RGB color (each channel typically in [0, 1], but
// not clamped — the consumer decides how to handle out-of-gamut values)
// given an object-space sample position. Compiled samplers are
// reentrant: calling Sample concurrently from multiple goroutines is
// safe because each call uses its own scratch slot table.
type Sampler interface {
	Sample(pos [3]float64) [3]float64
}

// MaterialNames returns the material names defined by the document, in
// alphabetical order.
func (d *Document) MaterialNames() []string {
	names := make([]string, 0, len(d.Materials))
	for k := range d.Materials {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// BaseColorSampler returns a Sampler for the base_color input of the
// surface shader bound to the named material.
func (d *Document) BaseColorSampler(materialName string) (Sampler, error) {
	m, ok := d.Materials[materialName]
	if !ok {
		return nil, fmt.Errorf("materialx: material %q not found", materialName)
	}
	if m.SurfaceShaderName == "" {
		return nil, fmt.Errorf("materialx: material %q has no surfaceshader binding", materialName)
	}
	s, ok := d.Surfaces[m.SurfaceShaderName]
	if !ok {
		return nil, fmt.Errorf("materialx: surface shader %q not found", m.SurfaceShaderName)
	}
	bc, ok := s.Inputs["base_color"]
	if !ok {
		return nil, fmt.Errorf("materialx: surface shader %q has no base_color input", s.Name)
	}
	return d.samplerFromInput(bc)
}

// DefaultBaseColorSampler returns a Sampler for the first material in
// alphabetical order. Documents typically contain a single material.
func (d *Document) DefaultBaseColorSampler() (Sampler, error) {
	names := d.MaterialNames()
	if len(names) == 0 {
		return nil, errors.New("materialx: document has no materials")
	}
	return d.BaseColorSampler(names[0])
}

func (d *Document) samplerFromInput(in *input) (Sampler, error) {
	if in.Value != nil {
		return constSampler(in.Value.AsVec3()), nil
	}
	if in.GraphName != "" {
		ng, ok := d.NodeGraphs[in.GraphName]
		if !ok {
			return nil, fmt.Errorf("materialx: nodegraph %q not found", in.GraphName)
		}
		outName := in.OutputName
		if outName == "" {
			outName = "out"
		}
		out, ok := ng.outputsByName[outName]
		if !ok {
			return nil, fmt.Errorf("materialx: nodegraph %q has no output %q", ng.Name, outName)
		}
		return compileGraph(ng, out)
	}
	return nil, fmt.Errorf("materialx: input %q has no usable source", in.Name)
}

type constSampler [3]float64

func (c constSampler) Sample(_ [3]float64) [3]float64 { return [3]float64(c) }

// --- compiled graph evaluator ---
//
// At construction time, every reachable node is lowered into a closure
// of type evalFn that reads pre-resolved input values (either pulled
// from the slot table or returned as constants). Slots are indexed by
// node position; the per-Sample scratch is a stack-allocated array of
// up to slotMax entries, with a heap fallback for larger graphs. There
// are no maps, no string lookups, and no allocations on the hot path.

type evalFn func(pos [3]float64, scratch []Value) Value

type compiledGraph struct {
	nSlots  int
	outSlot int
	steps   []compileStep
}

type compileStep struct {
	slot int
	fn   evalFn
}

// slotMax sets the pooled scratch capacity. Real-world procedural
// graphs (marble: 14 nodes; brick/wood: ~30) fit comfortably; larger
// graphs fall through to a per-call allocation. The closure call sites
// cause escape analysis to give up on a stack-allocated array, so
// pooling delivers the zero-alloc fast path while preserving reentrancy.
const slotMax = 64

var scratchPool = sync.Pool{
	New: func() any {
		s := make([]Value, slotMax)
		return &s
	},
}

func (g *compiledGraph) Sample(pos [3]float64) [3]float64 {
	if g.nSlots > slotMax {
		scratch := make([]Value, g.nSlots)
		return g.run(pos, scratch)
	}
	p := scratchPool.Get().(*[]Value)
	out := g.run(pos, (*p)[:g.nSlots])
	scratchPool.Put(p)
	return out
}

func (g *compiledGraph) run(pos [3]float64, scratch []Value) [3]float64 {
	for _, st := range g.steps {
		scratch[st.slot] = st.fn(pos, scratch)
	}
	return scratch[g.outSlot].AsVec3()
}

// compileGraph walks the graph from the named output and lowers every
// reachable node into an ordered list of evalFn closures. Returns an
// error if any node references an unknown type, missing input, or
// unsupported attribute value — eager validation is exhaustive (every
// reachable node is compiled), unlike the prior approach of sampling
// once at the origin.
func compileGraph(ng *nodeGraph, out *graphOutput) (Sampler, error) {
	if out.NodeName == "" {
		return nil, fmt.Errorf("materialx: nodegraph %q output %q has no nodename", ng.Name, out.Name)
	}
	c := &compiler{
		ng:      ng,
		slotOf:  map[string]int{},
		visited: map[string]bool{},
	}
	outSlot, err := c.compileNode(out.NodeName)
	if err != nil {
		return nil, err
	}
	return &compiledGraph{
		nSlots:  c.nSlots,
		outSlot: outSlot,
		steps:   c.steps,
	}, nil
}

type compiler struct {
	ng      *nodeGraph
	slotOf  map[string]int
	visited map[string]bool // detects cycles
	steps   []compileStep
	nSlots  int
}

func (c *compiler) compileNode(name string) (int, error) {
	if slot, ok := c.slotOf[name]; ok {
		return slot, nil
	}
	if c.visited[name] {
		return 0, fmt.Errorf("materialx: cycle through node %q", name)
	}
	c.visited[name] = true

	n, ok := c.ng.nodesByName[name]
	if !ok {
		return 0, fmt.Errorf("materialx: node %q not found in nodegraph %q", name, c.ng.Name)
	}

	build, ok := nodeBuilders[n.Type]
	if !ok {
		return 0, fmt.Errorf("materialx: unsupported node type %q (node %q)", n.Type, n.Name)
	}
	fn, err := build(c, n)
	if err != nil {
		return 0, fmt.Errorf("node %q (%s): %w", n.Name, n.Type, err)
	}

	slot := c.nSlots
	c.nSlots++
	c.slotOf[name] = slot
	c.steps = append(c.steps, compileStep{slot: slot, fn: fn})
	return slot, nil
}

// compileInput resolves an input wire to an evalFn that produces its
// value during Sample. Three sources: another node (recurse and emit a
// slot read), a graph-level interface parameter (emit a constant from
// its default), or a literal value (emit a constant).
func (c *compiler) compileInput(in *input) (evalFn, error) {
	switch {
	case in.NodeName != "":
		slot, err := c.compileNode(in.NodeName)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", in.Name, err)
		}
		return func(_ [3]float64, scratch []Value) Value { return scratch[slot] }, nil
	case in.InterfaceName != "":
		gi, ok := c.ng.inputsByName[in.InterfaceName]
		if !ok {
			return nil, fmt.Errorf("input %q references missing interface %q", in.Name, in.InterfaceName)
		}
		v := gi.Default
		return func(_ [3]float64, _ []Value) Value { return v }, nil
	case in.Value != nil:
		v := *in.Value
		return func(_ [3]float64, _ []Value) Value { return v }, nil
	}
	return nil, fmt.Errorf("input %q has no value", in.Name)
}

// compileOptional returns nil if the input is absent (and the default
// will be used at call sites). Non-nil errors propagate.
func (c *compiler) compileOptional(n *node, name string) (evalFn, error) {
	in, ok := n.inputsByName[name]
	if !ok {
		return nil, nil
	}
	return c.compileInput(in)
}

func (c *compiler) compileRequired(n *node, name string) (evalFn, error) {
	in, ok := n.inputsByName[name]
	if !ok {
		return nil, fmt.Errorf("missing required input %q", name)
	}
	return c.compileInput(in)
}

// --- node builders ---

type nodeBuilder func(c *compiler, n *node) (evalFn, error)

// Populated in init() to break the initialization cycle: each builder
// transitively reads nodeBuilders during recursion.
var nodeBuilders map[string]nodeBuilder

func init() {
	nodeBuilders = map[string]nodeBuilder{
		"position":   buildPosition,
		"constant":   buildConstant,
		"dotproduct": buildDotProduct,
		"multiply":   buildArithmetic(func(a, b float64) float64 { return a * b }),
		"add":        buildArithmetic(func(a, b float64) float64 { return a + b }),
		"subtract":   buildArithmetic(func(a, b float64) float64 { return a - b }),
		"fractal3d":  buildFractal3D,
		"noise3d":    buildNoise3D,
		"sin":        buildUnary(math.Sin),
		"cos":        buildUnary(math.Cos),
		"power":      buildPower,
		"clamp":      buildClamp,
		"mix":        buildMix,
	}
}

func buildPosition(_ *compiler, n *node) (evalFn, error) {
	// "space" attribute: only object-space is supported. Anything else
	// would silently produce wrong results since the caller hands us
	// coordinates in a single fixed frame.
	if in, ok := n.inputsByName["space"]; ok && in.Value != nil {
		return nil, fmt.Errorf("position node: only object-space is supported, got %v", in.Value)
	}
	return func(pos [3]float64, _ []Value) Value { return Vec3Value(pos) }, nil
}

func buildConstant(c *compiler, n *node) (evalFn, error) {
	return c.compileRequired(n, "value")
}

func buildDotProduct(c *compiler, n *node) (evalFn, error) {
	a, err := c.compileRequired(n, "in1")
	if err != nil {
		return nil, err
	}
	b, err := c.compileRequired(n, "in2")
	if err != nil {
		return nil, err
	}
	return func(pos [3]float64, scratch []Value) Value {
		av := a(pos, scratch).AsVec3()
		bv := b(pos, scratch).AsVec3()
		return FloatValue(av[0]*bv[0] + av[1]*bv[1] + av[2]*bv[2])
	}, nil
}

func buildArithmetic(op func(a, b float64) float64) nodeBuilder {
	return func(c *compiler, n *node) (evalFn, error) {
		a, err := c.compileRequired(n, "in1")
		if err != nil {
			return nil, err
		}
		b, err := c.compileRequired(n, "in2")
		if err != nil {
			return nil, err
		}
		out := n.OutputType
		arity := vecArity(out)
		switch out {
		case TypeFloat:
			return func(pos [3]float64, scratch []Value) Value {
				return FloatValue(op(a(pos, scratch).AsFloat(), b(pos, scratch).AsFloat()))
			}, nil
		case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
			return func(pos [3]float64, scratch []Value) Value {
				av := broadcast(a(pos, scratch))
				bv := broadcast(b(pos, scratch))
				v := Value{Type: out}
				for i := range arity {
					v.Vec[i] = op(av[i], bv[i])
				}
				return v
			}, nil
		}
		return nil, fmt.Errorf("unsupported output type %s", out)
	}
}

func buildFractal3D(c *compiler, n *node) (evalFn, error) {
	posFn, err := c.compileOptional(n, "position")
	if err != nil {
		return nil, err
	}
	octFn, err := c.compileOptional(n, "octaves")
	if err != nil {
		return nil, err
	}
	lacFn, err := c.compileOptional(n, "lacunarity")
	if err != nil {
		return nil, err
	}
	dimFn, err := c.compileOptional(n, "diminish")
	if err != nil {
		return nil, err
	}
	ampFn, err := c.compileOptional(n, "amplitude")
	if err != nil {
		return nil, err
	}
	return func(pos [3]float64, scratch []Value) Value {
		p := posOrDefault(posFn, pos, scratch)
		oct := intOrDefault(octFn, pos, scratch, 3)
		lac := floatOrDefault(lacFn, pos, scratch, 2.0)
		dim := floatOrDefault(dimFn, pos, scratch, 0.5)
		amp := floatOrDefault(ampFn, pos, scratch, 1.0)
		return FloatValue(amp * fractal3D(p[0], p[1], p[2], oct, lac, dim))
	}, nil
}

func buildNoise3D(c *compiler, n *node) (evalFn, error) {
	posFn, err := c.compileOptional(n, "position")
	if err != nil {
		return nil, err
	}
	ampFn, err := c.compileOptional(n, "amplitude")
	if err != nil {
		return nil, err
	}
	pivFn, err := c.compileOptional(n, "pivot")
	if err != nil {
		return nil, err
	}
	return func(pos [3]float64, scratch []Value) Value {
		p := posOrDefault(posFn, pos, scratch)
		amp := floatOrDefault(ampFn, pos, scratch, 1.0)
		piv := floatOrDefault(pivFn, pos, scratch, 0.0)
		return FloatValue(perlin3D(p[0], p[1], p[2])*amp + piv)
	}, nil
}

func buildUnary(op func(float64) float64) nodeBuilder {
	return func(c *compiler, n *node) (evalFn, error) {
		in, err := c.compileRequired(n, "in")
		if err != nil {
			return nil, err
		}
		return func(pos [3]float64, scratch []Value) Value {
			return FloatValue(op(in(pos, scratch).AsFloat()))
		}, nil
	}
}

func buildPower(c *compiler, n *node) (evalFn, error) {
	a, err := c.compileRequired(n, "in1")
	if err != nil {
		return nil, err
	}
	b, err := c.compileRequired(n, "in2")
	if err != nil {
		return nil, err
	}
	return func(pos [3]float64, scratch []Value) Value {
		return FloatValue(math.Pow(a(pos, scratch).AsFloat(), b(pos, scratch).AsFloat()))
	}, nil
}

func buildClamp(c *compiler, n *node) (evalFn, error) {
	in, err := c.compileRequired(n, "in")
	if err != nil {
		return nil, err
	}
	lowFn, err := c.compileOptional(n, "low")
	if err != nil {
		return nil, err
	}
	highFn, err := c.compileOptional(n, "high")
	if err != nil {
		return nil, err
	}
	out := n.OutputType
	arity := vecArity(out)
	if out == TypeFloat {
		return func(pos [3]float64, scratch []Value) Value {
			low := floatOrDefault(lowFn, pos, scratch, 0)
			high := floatOrDefault(highFn, pos, scratch, 1)
			return FloatValue(clampF(in(pos, scratch).AsFloat(), low, high))
		}, nil
	}
	return func(pos [3]float64, scratch []Value) Value {
		low := floatOrDefault(lowFn, pos, scratch, 0)
		high := floatOrDefault(highFn, pos, scratch, 1)
		v := Value{Type: out}
		src := in(pos, scratch).Vec
		for i := range arity {
			v.Vec[i] = clampF(src[i], low, high)
		}
		return v
	}, nil
}

// buildMix implements MaterialX mix(bg, fg, t) = bg*(1-t) + fg*t. Per
// spec, scalar inputs are broadcast to the output arity (e.g. a float
// fed into a color3 mix produces a constant grey).
func buildMix(c *compiler, n *node) (evalFn, error) {
	bg, err := c.compileRequired(n, "bg")
	if err != nil {
		return nil, err
	}
	fg, err := c.compileRequired(n, "fg")
	if err != nil {
		return nil, err
	}
	mixFn, err := c.compileRequired(n, "mix")
	if err != nil {
		return nil, err
	}
	out := n.OutputType
	arity := vecArity(out)
	switch out {
	case TypeFloat:
		return func(pos [3]float64, scratch []Value) Value {
			t := mixFn(pos, scratch).AsFloat()
			return FloatValue(bg(pos, scratch).AsFloat()*(1-t) + fg(pos, scratch).AsFloat()*t)
		}, nil
	case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
		return func(pos [3]float64, scratch []Value) Value {
			t := mixFn(pos, scratch).AsFloat()
			bgv := broadcast(bg(pos, scratch))
			fgv := broadcast(fg(pos, scratch))
			v := Value{Type: out}
			for i := range arity {
				v.Vec[i] = bgv[i]*(1-t) + fgv[i]*t
			}
			return v
		}, nil
	}
	return nil, fmt.Errorf("unsupported mix output type %s", out)
}

// --- helpers ---

// broadcast widens a scalar Value into a 4-component vector by
// replicating the scalar; vector/color values pass through unchanged.
func broadcast(v Value) [4]float64 {
	if v.Type == TypeFloat || v.Type == TypeInteger {
		f := v.AsFloat()
		return [4]float64{f, f, f, f}
	}
	return v.Vec
}

func posOrDefault(fn evalFn, pos [3]float64, scratch []Value) [3]float64 {
	if fn == nil {
		return pos
	}
	return fn(pos, scratch).AsVec3()
}

func intOrDefault(fn evalFn, pos [3]float64, scratch []Value, def int) int {
	if fn == nil {
		return def
	}
	return fn(pos, scratch).AsInt()
}

func floatOrDefault(fn evalFn, pos [3]float64, scratch []Value, def float64) float64 {
	if fn == nil {
		return def
	}
	return fn(pos, scratch).AsFloat()
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
