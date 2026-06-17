# Plan: single-source-of-truth run state for the model viewers

## Problem

The output (and input) model viewers occasionally show stale or not-updated
content when a new model is opened — most visibly "the output model is not
updated while the second model is being loaded." This is a **race**, not a
one-off, and it is a symptom of how viewer state is organised.

### Root cause (confirmed by instrumentation)

The frontend's staleness gate, `latestGen`, is a **lagging async copy** of the
backend's generation counter. In `runPipeline`:

```js
const gen = await ProcessPipeline(buildOpts(force)); // backend bumps gen + cancels prior run synchronously
latestGen = gen;                                      // frontend records it only AFTER the IPC round-trip
```

The backend assigns the generation **synchronously** (`app.go` —
`gen := a.pipeGen.Add(1)`), cancels the previous run, and **begins emitting
events for the new gen during the IPC round-trip** — before `latestGen` is
updated.

Trace evidence (`/tmp/stale-dbg.log`, 2026-06-16):

- Run 1: backend emitted `output-preview-mesh gen=1` at 21.190, but the frontend
  set `latestGen=1` only at 21.221 — a **31 ms window** where the gate read `0`
  while the backend was already running gen 1.
- Run 2: the same gap was **2 ms**.

So the window between "backend starts generation N" and "frontend records
`latestGen = N`" is **non-deterministic, ~2–36 ms** (the `ProcessPipeline` IPC
latency). Every event handler gates with `if (event.gen < latestGen) return`.
During the window the gate is stale, so:

- A late event from the **superseded** run (opening model 2 while model 1 is
  still in flight) can satisfy `oldGen < staleLatestGen` and be **APPLIED** →
  stale mesh written into the viewer, which then persists through the new
  model's load.
- Symmetrically, a valid new-gen event arriving in an unlucky ordering can be
  dropped.

In strictly-sequential use with an idle gap (cube fully finished 9 s before
Orzel was opened) there are no in-flight other-gen events, so it is benign —
which is exactly why the bug is intermittent.

### Why it is a *class* of bug

"What the viewer shows" is **pushed** into ~9 independent mutable `$state`
variables — `inputMeshUrl`, `inputOverlayMeshUrl`, `wrappedMeshUrl`,
`outputMeshUrl`, `outputPreviewMeshUrl`, `running`, `pipelineStages`,
`pipelineError`, `latestGen` — written from ~12 event handlers plus
`clearViewportMesh()` and `runPipeline()`. Each transition must *remember* to
reset each variable, and each handler re-implements the gen guard by hand.
Adding a feature (the gray output preview) added a 9th variable and three more
reset/gate sites — which is why this surfaced now. Nothing structurally
prevents a stale write.

## Target design

Make stale writes **structurally impossible** and make the display **derived**,
not pushed.

1. **Own the generation on the frontend, synchronously.** Allocate a monotonic
   `runId` at the instant a run is requested, *before* any `await`. Pass it to
   the backend as a correlation id (the backend echoes it on every event)
   instead of learning the gen back from `ProcessPipeline`. The gate is then
   always current — there is no window.

2. **One record = the whole viewer state, replaced atomically per run.**

   ```ts
   type RunView = {
     id: number;                         // the owning runId
     phase: 'idle' | 'running' | 'done' | 'error';
     stages: StageInfo[];
     error: string;
     input: { meshUrl?: string; overlayUrl?: string; wrappedUrl?: string };
     output: { previewUrl?: string; finalUrl?: string };
   };
   let run = $state<RunView>(IDLE);
   ```

   Starting a run does `run = { id: ++runId, phase: 'running', stages: [], ... }`
   — a single atomic replacement. There are no scattered per-variable resets to
   forget; the record *is* the state.

3. **Exact-match gate, not `<`.**

   ```ts
   if (event.gen !== run.id) return;   // any other generation cannot touch the current record
   ```

   An event from an older *or* a torn-down newer run is simply ignored.

4. **Derive the display.**

   ```ts
   const outputMesh  = $derived(run.output.finalUrl ?? run.output.previewUrl);
   const showOverlay = $derived(run.phase === 'running' || !!run.error);
   ```

   `ModelViewer` stops re-deriving `?? ` / `running`-gating; App owns one truth.

## Migration steps

1. **Confirm + remove instrumentation.** The `[STALE-DBG]` logging in
   `app.go` and `App.svelte` (and the `dbg()` helper) was added for this
   investigation; remove it once the refactor lands.
2. Introduce the `RunView` type and a single `run` `$state`. Allocate `runId`
   synchronously in `runPipeline` before `await`; thread it to the backend as a
   correlation id (extend `pipeline.Options` or the `ProcessPipeline`
   signature) so events echo the frontend-owned id. Keep the backend's own
   `pipeGen` for cancel/drain bookkeeping if convenient, but the **frontend id
   is the gate**.
3. Rewrite the event handlers to `if (event.gen !== run.id) return;` and to
   mutate fields on `run` only.
4. Replace `clearViewportMesh()` + the scattered resets in `runPipeline()` with
   the single `run = { id: ++runId, phase: 'running', ... }` replacement.
5. Replace the viewer props with derived values (`outputMesh`, `showOverlay`,
   input equivalents). Simplify `ModelViewer` so it no longer owns
   `progressActive`/`?? ` display logic beyond rendering what it is given.
6. Verify: `go test ./...`, `npm run check`, and a manual overlap repro (open
   model 2 while model 1 is still processing) — the exact-match gate must drop
   the superseded run's late events, and the viewer must never show a stale
   mesh.

## Notes / open questions

- The backend currently emits `pipeline-cancelled` with **no frontend handler**.
  Under the new model that event is irrelevant (its gen won't match `run.id`),
  but consider handling it to set `phase` cleanly if a run is cancelled without
  a successor.
- Separate, lower-priority observation from the same trace: even on the happy
  path, the output viewer is blank for ~10 s after opening a large model
  because the first gray preview waits for parse + decimation. If faster output
  feedback is wanted, emit an immediate output preview from the input geometry
  at `StagePreload`. Out of scope for this fix; noted for later.
