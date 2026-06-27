package pipeline

import (
	"fmt"
	"hash"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// StageDef declares everything the pipeline driver needs to know about
// one stage. The stageDefs table below is the single source of truth
// for the stage graph: adding a stage means adding one entry (plus its
// body and output type) — dependency resolution, caching, memoization,
// progress markers, cancellation checks, and panic containment are all
// generic driver behavior (see resolve and runStageCached).
type StageDef struct {
	ID StageID
	// Name is the human-readable progress label ("Voxelizing").
	Name string
	// Subdir is the disk-cache subdirectory for this stage's blobs.
	Subdir string
	// Deps are the upstream stages this stage's body consumes. The
	// driver resolves them (recursively) before invoking Run — but
	// only on a cache miss, so a warm cache never touches upstream
	// stages that nothing else demands.
	Deps []StageID
	// Alloc returns a fresh, zero-valued pointer of the stage's
	// output type, suitable for gob-decoding a disk blob into. The
	// concrete type matters: gob.Decode needs it behind the any.
	Alloc func() any
	// Description summarizes an entry for the disk-cache meta sidecar
	// ("Load: foo.glb (alpha-wrap)" beats an opaque hash in sweep
	// printouts).
	Description func(opts Options) string
	// HashSettings writes the fingerprint of every option that
	// affects this stage's output. The driver folds it into the
	// stage's cumulative cache key (see StageCache.stageKey). MUST
	// be byte-stable across releases — changing what or how this
	// writes invalidates user caches; to do that deliberately, write
	// a salt string first (see the voxelize/dither hashers).
	HashSettings func(c *StageCache, h hash.Hash64, opts Options)
	// Run computes the stage output on a cache miss. Deps are already
	// resolved and memoized, so the body's typed accessor calls
	// (r.Load(), r.Voxelize(), …) return instantly. Bodies own their
	// progress reporting (labels and totals are stage-specific).
	Run func(r *pipelineRun) (any, error)
	// After, when non-nil, runs on EVERY resolve — fresh compute,
	// disk hit, and memo hit alike. Used for cheap idempotent
	// post-processing that tracks Options outside the cache key
	// (Load's in-place base-color rebake).
	After func(r *pipelineRun, out any) error
}

// stageDefs is the pipeline's stage table, indexed by StageID. Order
// matters twice: the index must equal the entry's ID (checked in
// init), and stageKey folds the per-stage settings hashes
// cumulatively in ID order, so IDs must be topologically ordered
// (every stage after all of its Deps) — which they are, by
// construction of the StageID enum.
//
// Filled by init() rather than a var initializer: the stage bodies
// reference stageNames/stageDefs themselves, and a package-level
// initializer expression that (transitively) mentions them trips Go's
// conservative initialization-cycle detection. Assignment inside
// init() sidesteps that without weakening anything — the init checks
// below still run before any use.
var stageDefs [numStages]StageDef

// stageNames maps StageID to its progress label. Derived from
// stageDefs in init(); kept as a map because many call sites predate
// the table.
var stageNames = make(map[StageID]string, numStages)

func init() {
	stageDefs = [numStages]StageDef{
		{
			ID:           StageParse,
			Name:         "Parsing",
			Subdir:       "parse",
			Deps:         nil,
			Alloc:        func() any { return &loader.LoadedModel{} },
			Description:  describeParse,
			HashSettings: hashParseSettings,
			Run:          (*pipelineRun).runParse,
		},
		{
			ID:           StagePreload,
			Name:         "Loading",
			Subdir:       "preload",
			Deps:         []StageID{StageParse},
			Alloc:        func() any { return &preloadOutput{} },
			Description:  describePreload,
			HashSettings: hashPreloadSettings,
			Run:          (*pipelineRun).runPreload,
		},
		{
			ID:           StageLoad,
			Name:         "Preparing geometry",
			Subdir:       "load",
			Deps:         []StageID{StagePreload},
			Alloc:        func() any { return &loadOutput{} },
			Description:  describeLoad,
			HashSettings: hashLoadSettings,
			Run:          (*pipelineRun).runLoad,
			// Reapply the base-color override on every resolve: the
			// override is excluded from Load's cache key (it mutates
			// FaceBaseColor in place), so a cached loadOutput must be
			// re-baked whenever the active override differs. Idempotent
			// and cheap when nothing changed.
			After: afterLoad,
		},
		{
			ID:           StageSplit,
			Name:         "Splitting",
			Subdir:       "split",
			Deps:         []StageID{StageLoad},
			Alloc:        func() any { return &splitOutput{} },
			Description:  describeSplit,
			HashSettings: hashSplitSettings,
			Run:          (*pipelineRun).runSplit,
		},
		{
			ID:           StageSticker,
			Name:         "Applying stickers",
			Subdir:       "sticker",
			Deps:         []StageID{StageLoad},
			Alloc:        func() any { return &stickerOutput{} },
			Description:  describeSticker,
			HashSettings: hashStickerSettings,
			Run:          (*pipelineRun).runSticker,
		},
		{
			ID:           StageVoxelize,
			Name:         "Voxelizing",
			Subdir:       "voxelize",
			Deps:         []StageID{StageLoad, StageSticker, StageSplit},
			Alloc:        func() any { return &voxelizeOutput{} },
			Description:  describeVoxelize,
			HashSettings: hashVoxelizeSettings,
			Run:          (*pipelineRun).runVoxelize,
		},
		{
			ID:           StagePalette,
			Name:         "Building palette",
			Subdir:       "palette",
			Deps:         []StageID{StageVoxelize},
			Alloc:        func() any { return &paletteOutput{} },
			Description:  describePalette,
			HashSettings: hashPaletteSettings,
			Run:          (*pipelineRun).runPalette,
		},
		{
			ID:           StageDither,
			Name:         "Dithering",
			Subdir:       "dither",
			Deps:         []StageID{StagePalette, StageVoxelize},
			Alloc:        func() any { return &ditherOutput{} },
			Description:  describeDither,
			HashSettings: hashDitherSettings,
			Run:          (*pipelineRun).runDither,
		},
		{
			ID:           StageClip,
			Name:         "Clipping",
			Subdir:       "clip",
			Deps:         []StageID{StageDither, StageVoxelize, StageLoad, StageSplit},
			Alloc:        func() any { return &clipOutput{} },
			Description:  describeClip,
			HashSettings: hashClipSettings,
			Run:          (*pipelineRun).runClip,
		},
		{
			ID:           StageMerge,
			Name:         "Merging",
			Subdir:       "merge",
			Deps:         []StageID{StageClip},
			Alloc:        func() any { return &mergeOutput{} },
			Description:  describeMerge,
			HashSettings: hashMergeSettings,
			Run:          (*pipelineRun).runMerge,
		},
	}

	for i := range stageDefs {
		stageNames[stageDefs[i].ID] = stageDefs[i].Name
	}

	// The table is indexed by StageID; a misordered entry would send
	// every lookup to the wrong stage. Fail loudly at startup rather
	// than corrupting caches at runtime.
	for i := range stageDefs {
		if stageDefs[i].ID != StageID(i) {
			panic(fmt.Sprintf("stageDefs[%d] declares ID %d; table must be ordered by StageID", i, stageDefs[i].ID))
		}
		if stageDefs[i].Run == nil || stageDefs[i].Alloc == nil || stageDefs[i].HashSettings == nil || stageDefs[i].Description == nil {
			panic(fmt.Sprintf("stageDefs[%d] (%s) is missing a required field", i, stageDefs[i].Name))
		}
		for _, d := range stageDefs[i].Deps {
			if d >= StageID(i) {
				panic(fmt.Sprintf("stageDefs[%d] (%s) depends on %d, which does not precede it; StageID order must be topological", i, stageDefs[i].Name, d))
			}
		}
	}
}

// resolve produces the output of one stage, demand-driven:
//
//  1. Per-run memo hit → return immediately (after the After hook).
//  2. Cache hit (disk) → decode, memoize, emit a UI marker; the body
//     and its dependencies never run.
//  3. Miss → resolve Deps recursively, run the body, memoize, and
//     async-write the blob to the disk cache.
//
// Dependency resolution happens INSIDE the miss path on purpose: on a
// fully-warm cache, asking for Merge touches only Merge's blob —
// Voxelize/Dither/Clip stay on disk untouched. Top-level callers ask
// for the outputs they need (Load/Sticker for previews, Palette/Merge
// for export) and the transitive closure loads only when something
// actually misses.
//
// The memo-before-cache.set ordering is load-bearing: cache.set
// encodes asynchronously, and same-run consumers must see the live
// pointer rather than racing the disk-write goroutine.
func (r *pipelineRun) resolve(id StageID) (any, error) {
	def := &stageDefs[id]
	if v, ok := r.memo[id]; ok {
		return r.runAfter(def, v)
	}
	if err := r.checkCancel(); err != nil {
		return nil, err
	}
	cached, err := runStageCached(r.cache, id, r.opts, r.tracker, func() error {
		for _, dep := range def.Deps {
			if _, err := r.resolve(dep); err != nil {
				return err
			}
		}
		if err := r.checkCancel(); err != nil {
			return err
		}
		out, err := def.Run(r)
		if err != nil {
			return err
		}
		r.memoize(id, out)
		r.cache.set(id, r.opts, out)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cached != nil {
		// Cache-hit path: stash the wrapper's already-decoded value
		// instead of doing a second cache.get. A second get would race
		// the background disk-cache sweep (kicked at the end of every
		// pipeline run) and could observe the file as deleted.
		r.memoize(id, cached)
	}
	v, ok := r.memo[id]
	if !ok || v == nil {
		// Defensive: succeeded with neither a cache hit nor a body
		// that memoized a result. Should be unreachable; surface
		// loudly rather than hand a nil to downstream consumers.
		return nil, fmt.Errorf("pipeline: stage %s succeeded with no result (cache file vanished?)", def.Name)
	}
	return r.runAfter(def, v)
}

func (r *pipelineRun) runAfter(def *StageDef, v any) (any, error) {
	if def.After != nil {
		if err := def.After(r, v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (r *pipelineRun) memoize(id StageID, v any) {
	if r.memo == nil {
		r.memo = make(map[StageID]any, numStages)
	}
	r.memo[id] = v
}

// resolveTyped is the typed face of resolve: the per-stage accessors
// (r.Load, r.Voxelize, …) call through here so stage bodies and
// top-level orchestration get concrete types back.
func resolveTyped[T any](r *pipelineRun, id StageID) (*T, error) {
	v, err := r.resolve(id)
	if err != nil {
		return nil, err
	}
	out, ok := v.(*T)
	if !ok {
		// Only reachable if a stageDefs entry's Alloc or Run returns
		// the wrong type — a programming error, not a runtime state.
		return nil, fmt.Errorf("pipeline: stage %s resolved to %T, want %T", stageDefs[id].Name, v, (*T)(nil))
	}
	return out, nil
}
