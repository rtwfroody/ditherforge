package materialx

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

// SampleContext bundles the per-sample inputs an evaluator may read.
// Procedural graphs only consume Pos; image-backed graphs additionally
// read UV (via the texcoord node) and Normal (the consumer typically
// uses this to drive triplanar projection in a wrapper, not inside the
// evaluator). Construct via Sampler.Sample for the legacy Pos-only
// path or Sampler.SampleAt for full control.
type SampleContext struct {
	Pos    [3]float64
	UV     [2]float64
	Normal [3]float64
}

// Sampler returns an RGB color (each channel typically in [0, 1], but
// not clamped — the consumer decides how to handle out-of-gamut values).
// Compiled samplers are reentrant: calling Sample/SampleAt concurrently
// from multiple goroutines is safe because each call uses its own
// scratch slot table.
//
// UsesUV reports whether the underlying graph reads SampleContext.UV
// (true for image-backed graphs and any graph containing a texcoord
// node). Consumers can use this to skip wrapping in triplanar
// projection when the graph is purely position-driven (e.g. marble),
// avoiding 3× redundant evaluator calls per sample.
type Sampler interface {
	Sample(pos [3]float64) [3]float64
	SampleAt(ctx SampleContext) [3]float64
	UsesUV() bool
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
		return compileGraph(ng, out, d.Resolver)
	}
	return nil, fmt.Errorf("materialx: input %q has no usable source", in.Name)
}

type constSampler [3]float64

func (c constSampler) Sample(_ [3]float64) [3]float64      { return [3]float64(c) }
func (c constSampler) SampleAt(_ SampleContext) [3]float64 { return [3]float64(c) }
func (c constSampler) UsesUV() bool                        { return false }

// --- compiled graph evaluator ---
//
// At construction time, every reachable node is lowered into a closure
// of type evalFn that reads pre-resolved input values (either pulled
// from the slot table or returned as constants). Slots are indexed by
// node position; the per-Sample scratch is a stack-allocated array of
// up to slotMax entries, with a heap fallback for larger graphs. There
// are no maps, no string lookups, and no allocations on the hot path.

type evalFn func(ctx *SampleContext, scratch []Value) Value

type compiledGraph struct {
	nSlots  int
	outSlot int
	steps   []compileStep
	usesUV  bool
}

func (g *compiledGraph) UsesUV() bool { return g.usesUV }

type compileStep struct {
	slot int
	fn   evalFn
}

// slotMax sets the pooled scratch capacity. Real-world graphs (marble:
// 14 nodes; brick/wood: ~30; PBR pack with image+texcoord+multiply:
// ~10) fit comfortably; larger graphs fall through to a per-call
// allocation. The closure call sites cause escape analysis to give up
// on a stack-allocated array, so pooling delivers the zero-alloc fast
// path while preserving reentrancy.
const slotMax = 64

// sampleScratch bundles the per-call context and slot table into a
// single pool-managed struct. Pooling both together keeps SampleContext
// from escaping to the heap when its address is passed to closures —
// the pointer points into a pool entry, not a stack-local that
// outlives the call.
type sampleScratch struct {
	ctx     SampleContext
	scratch []Value
}

var scratchPool = sync.Pool{
	New: func() any {
		return &sampleScratch{scratch: make([]Value, slotMax)}
	},
}

func (g *compiledGraph) Sample(pos [3]float64) [3]float64 {
	return g.SampleAt(SampleContext{Pos: pos})
}

func (g *compiledGraph) SampleAt(ctx SampleContext) [3]float64 {
	if g.nSlots > slotMax {
		s := &sampleScratch{ctx: ctx, scratch: make([]Value, g.nSlots)}
		return g.run(s)
	}
	s := scratchPool.Get().(*sampleScratch)
	s.ctx = ctx
	out := g.run(s)
	// Zero the context so the next pool consumer doesn't inherit it.
	s.ctx = SampleContext{}
	scratchPool.Put(s)
	return out
}

func (g *compiledGraph) run(s *sampleScratch) [3]float64 {
	scratch := s.scratch[:g.nSlots]
	for _, st := range g.steps {
		scratch[st.slot] = st.fn(&s.ctx, scratch)
	}
	return scratch[g.outSlot].AsVec3()
}

// compileGraph walks the graph from the named output and lowers every
// reachable node into an ordered list of evalFn closures. Returns an
// error if any node references an unknown type, missing input, or
// unsupported attribute value — eager validation is exhaustive (every
// reachable node is compiled). When the graph references image files
// (image nodes), resolver must be non-nil; otherwise sampler
// construction fails with a clear error.
func compileGraph(ng *nodeGraph, out *graphOutput, resolver ResourceResolver) (Sampler, error) {
	if out.NodeName == "" {
		return nil, fmt.Errorf("materialx: nodegraph %q output %q has no nodename", ng.Name, out.Name)
	}
	c := &compiler{
		ng:       ng,
		slotOf:   map[string]int{},
		visited:  map[string]bool{},
		resolver: resolver,
		images:   newImageCache(),
	}
	outSlot, err := c.compileNode(out.NodeName)
	if err != nil {
		return nil, err
	}
	return &compiledGraph{
		nSlots:  c.nSlots,
		outSlot: outSlot,
		steps:   c.steps,
		usesUV:  c.usesUV,
	}, nil
}

type compiler struct {
	ng       *nodeGraph
	slotOf   map[string]int
	visited  map[string]bool // detects cycles
	steps    []compileStep
	nSlots   int
	resolver ResourceResolver
	images   *imageCache
	usesUV   bool
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
		return func(_ *SampleContext, scratch []Value) Value { return scratch[slot] }, nil
	case in.InterfaceName != "":
		gi, ok := c.ng.inputsByName[in.InterfaceName]
		if !ok {
			return nil, fmt.Errorf("input %q references missing interface %q", in.Name, in.InterfaceName)
		}
		v := gi.Default
		return func(_ *SampleContext, _ []Value) Value { return v }, nil
	case in.Value != nil:
		v := *in.Value
		return func(_ *SampleContext, _ []Value) Value { return v }, nil
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

// stringInputOrDefault reads a literal string-typed input by name,
// returning def if the input is absent or has no raw value. Used by
// builders that consume enum-style attributes (addressmode, filtertype).
func stringInputOrDefault(n *node, name, def string) string {
	in, ok := n.inputsByName[name]
	if !ok {
		return def
	}
	if in.RawString != "" {
		return in.RawString
	}
	return def
}

// --- node builders ---

type nodeBuilder func(c *compiler, n *node) (evalFn, error)

// Populated in init() to break the initialization cycle: each builder
// transitively reads nodeBuilders during recursion.
var nodeBuilders map[string]nodeBuilder

func init() {
	nodeBuilders = map[string]nodeBuilder{
		"position":   buildPosition,
		"texcoord":   buildTexcoord,
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
		"image":      buildImage,
		"extract":    buildExtract,
	}
}

func buildPosition(_ *compiler, n *node) (evalFn, error) {
	// "space" attribute: only object-space is supported. Anything else
	// would silently produce wrong results since the caller hands us
	// coordinates in a single fixed frame.
	if in, ok := n.inputsByName["space"]; ok && in.RawString != "" && in.RawString != "object" {
		return nil, fmt.Errorf("position node: only object-space is supported, got %q", in.RawString)
	}
	return func(ctx *SampleContext, _ []Value) Value { return Vec3Value(ctx.Pos) }, nil
}

func buildTexcoord(c *compiler, n *node) (evalFn, error) {
	c.usesUV = true
	// MaterialX texcoord exposes a UV channel index (default 0). We
	// only plumb a single UV channel through SampleContext.UV — any
	// non-zero index would silently return the same UV. Fail loudly
	// rather than mislead.
	if in, ok := n.inputsByName["index"]; ok && in.Value != nil {
		if in.Value.AsInt() != 0 {
			return nil, fmt.Errorf("texcoord node: only UV channel 0 is supported, got %d", in.Value.AsInt())
		}
	}
	return func(ctx *SampleContext, _ []Value) Value { return Vec2Value(ctx.UV) }, nil
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
	return func(ctx *SampleContext, scratch []Value) Value {
		av := a(ctx, scratch).AsVec3()
		bv := b(ctx, scratch).AsVec3()
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
			return func(ctx *SampleContext, scratch []Value) Value {
				return FloatValue(op(a(ctx, scratch).AsFloat(), b(ctx, scratch).AsFloat()))
			}, nil
		case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
			return func(ctx *SampleContext, scratch []Value) Value {
				av := broadcast(a(ctx, scratch))
				bv := broadcast(b(ctx, scratch))
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
	return func(ctx *SampleContext, scratch []Value) Value {
		p := posOrDefault(posFn, ctx, scratch)
		oct := intOrDefault(octFn, ctx, scratch, 3)
		lac := floatOrDefault(lacFn, ctx, scratch, 2.0)
		dim := floatOrDefault(dimFn, ctx, scratch, 0.5)
		amp := floatOrDefault(ampFn, ctx, scratch, 1.0)
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
	return func(ctx *SampleContext, scratch []Value) Value {
		p := posOrDefault(posFn, ctx, scratch)
		amp := floatOrDefault(ampFn, ctx, scratch, 1.0)
		piv := floatOrDefault(pivFn, ctx, scratch, 0.0)
		return FloatValue(perlin3D(p[0], p[1], p[2])*amp + piv)
	}, nil
}

func buildUnary(op func(float64) float64) nodeBuilder {
	return func(c *compiler, n *node) (evalFn, error) {
		in, err := c.compileRequired(n, "in")
		if err != nil {
			return nil, err
		}
		return func(ctx *SampleContext, scratch []Value) Value {
			return FloatValue(op(in(ctx, scratch).AsFloat()))
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
	return func(ctx *SampleContext, scratch []Value) Value {
		return FloatValue(math.Pow(a(ctx, scratch).AsFloat(), b(ctx, scratch).AsFloat()))
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
		return func(ctx *SampleContext, scratch []Value) Value {
			low := floatOrDefault(lowFn, ctx, scratch, 0)
			high := floatOrDefault(highFn, ctx, scratch, 1)
			return FloatValue(clampF(in(ctx, scratch).AsFloat(), low, high))
		}, nil
	}
	return func(ctx *SampleContext, scratch []Value) Value {
		low := floatOrDefault(lowFn, ctx, scratch, 0)
		high := floatOrDefault(highFn, ctx, scratch, 1)
		v := Value{Type: out}
		src := in(ctx, scratch).Vec
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
		return func(ctx *SampleContext, scratch []Value) Value {
			t := mixFn(ctx, scratch).AsFloat()
			return FloatValue(bg(ctx, scratch).AsFloat()*(1-t) + fg(ctx, scratch).AsFloat()*t)
		}, nil
	case TypeVector2, TypeVector3, TypeVector4, TypeColor3, TypeColor4:
		return func(ctx *SampleContext, scratch []Value) Value {
			t := mixFn(ctx, scratch).AsFloat()
			bgv := broadcast(bg(ctx, scratch))
			fgv := broadcast(fg(ctx, scratch))
			v := Value{Type: out}
			for i := range arity {
				v.Vec[i] = bgv[i]*(1-t) + fgv[i]*t
			}
			return v
		}, nil
	}
	return nil, fmt.Errorf("unsupported mix output type %s", out)
}

// buildImage loads the referenced texture once at compile time and
// returns a closure that samples it at the texcoord input's UV (or
// SampleContext.UV directly when no texcoord is wired). The output
// type drives how many channels are pulled from the texture; alpha is
// dropped because ditherforge bakes alpha separately. RGB stays in
// sRGB throughout — the consumer (voxel pipeline) wants sRGB-quantized
// output anyway, so a linearize-then-encode round-trip would only add
// rounding error.
func buildImage(c *compiler, n *node) (evalFn, error) {
	c.usesUV = true
	fileIn, ok := n.inputsByName["file"]
	if !ok {
		return nil, fmt.Errorf("image node: missing required %q input", "file")
	}
	if fileIn.RawString == "" {
		return nil, fmt.Errorf("image node: %q input has no path", "file")
	}
	img, err := c.images.load(c.resolver, fileIn.RawString, fileIn.Colorspace)
	if err != nil {
		return nil, fmt.Errorf("image node: %w", err)
	}
	uMode := parseAddressMode(stringInputOrDefault(n, "uaddressmode", "periodic"))
	vMode := parseAddressMode(stringInputOrDefault(n, "vaddressmode", "periodic"))
	filter := parseFilterType(stringInputOrDefault(n, "filtertype", "linear"))

	uvFn, err := c.compileOptional(n, "texcoord")
	if err != nil {
		return nil, err
	}

	out := n.OutputType
	arity := vecArity(out)
	// `default` input intentionally ignored: the only scenario the
	// MaterialX spec uses it for is "file load failed at runtime",
	// which we surface at compile time via images.load returning an
	// error. Out-of-bounds UVs are handled by the address-mode
	// inputs, not the default.

	return func(ctx *SampleContext, scratch []Value) Value {
		var uv [2]float64
		if uvFn != nil {
			v := uvFn(ctx, scratch)
			uv = [2]float64{v.Vec[0], v.Vec[1]}
		} else {
			uv = ctx.UV
		}
		rgb := img.sample(uv, uMode, vMode, filter)
		v := Value{Type: out}
		switch arity {
		case 0: // float
			v.Type = TypeFloat
			v.F = rgb[0]
			return v
		case 2:
			v.Vec[0] = rgb[0]
			v.Vec[1] = rgb[1]
		case 3:
			v.Vec[0] = rgb[0]
			v.Vec[1] = rgb[1]
			v.Vec[2] = rgb[2]
		case 4:
			v.Vec[0] = rgb[0]
			v.Vec[1] = rgb[1]
			v.Vec[2] = rgb[2]
			v.Vec[3] = 1
		}
		return v
	}, nil
}

// buildExtract pulls one component out of a vector/color, indexed by
// the literal `index` input (0-based).
func buildExtract(c *compiler, n *node) (evalFn, error) {
	in, err := c.compileRequired(n, "in")
	if err != nil {
		return nil, err
	}
	idxIn, ok := n.inputsByName["index"]
	if !ok || idxIn.Value == nil {
		return nil, fmt.Errorf("extract node: missing literal %q input", "index")
	}
	idx := idxIn.Value.AsInt()
	if idx < 0 || idx >= 4 {
		return nil, fmt.Errorf("extract node: index %d out of range [0, 4)", idx)
	}
	return func(ctx *SampleContext, scratch []Value) Value {
		v := in(ctx, scratch)
		return FloatValue(v.Vec[idx])
	}, nil
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

func posOrDefault(fn evalFn, ctx *SampleContext, scratch []Value) [3]float64 {
	if fn == nil {
		return ctx.Pos
	}
	return fn(ctx, scratch).AsVec3()
}

func intOrDefault(fn evalFn, ctx *SampleContext, scratch []Value, def int) int {
	if fn == nil {
		return def
	}
	return fn(ctx, scratch).AsInt()
}

func floatOrDefault(fn evalFn, ctx *SampleContext, scratch []Value, def float64) float64 {
	if fn == nil {
		return def
	}
	return fn(ctx, scratch).AsFloat()
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
