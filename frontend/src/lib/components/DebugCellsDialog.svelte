<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import { untrack } from 'svelte';
  import { DebugCellsSlabSVG } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let {
    open = $bindable(false),
  }: {
    open: boolean;
  } = $props();

  let slabIdx = $state(0);
  let slabCount = $state(0);
  let medianArea = $state(0);
  let svgMarkup = $state<string>('');
  let errorMsg = $state<string>('');
  let loading = $state(false);

  // Coalesce in-flight fetches: while one is running, just remember
  // the latest requested slabIdx and chase it as soon as the current
  // one resolves. This keeps the slider feeling immediate even though
  // each backend render takes some real time.
  let inFlight = false;
  let pendingIdx: number | null = null;

  async function fetchSlab(target: number) {
    if (inFlight) {
      pendingIdx = target;
      return;
    }
    inFlight = true;
    loading = true;
    try {
      while (true) {
        errorMsg = '';
        let r: main.DebugCellsSlabResult;
        try {
          r = (await DebugCellsSlabSVG(target)) as main.DebugCellsSlabResult;
        } catch (e: any) {
          errorMsg = e?.toString?.() ?? String(e);
          svgMarkup = '';
          break;
        }
        slabCount = r.slabCount;
        medianArea = r.medianCellAreaMM2 ?? 0;
        svgMarkup = r.svg ?? '';

        // If the user moved the slider while this request was in
        // flight, chase the latest position before yielding.
        if (pendingIdx !== null && pendingIdx !== target) {
          target = pendingIdx;
          pendingIdx = null;
          continue;
        }
        pendingIdx = null;
        break;
      }
    } finally {
      inFlight = false;
      loading = false;
    }
  }

  // Reset slab idx and fetch the first frame whenever the dialog
  // opens. Keep the prior slabIdx within bounds otherwise.
  let prevOpen = false;
  $effect(() => {
    if (open && !prevOpen) {
      slabIdx = 0;
      fetchSlab(0);
    }
    prevOpen = open;
  });

  // Re-fetch whenever the slab index changes while the dialog is open.
  // fetchSlab handles coalescing internally so this is safe to fire
  // on every drag tick.
  let prevIdx = -1;
  $effect(() => {
    if (open && slabIdx !== prevIdx) {
      prevIdx = slabIdx;
      fetchSlab(slabIdx);
    }
  });

  // --- Pan / zoom / scale-bar -------------------------------------------
  //
  // The SVG comes from the Go backend with a viewBox in mm. We mutate
  // that viewBox in place on wheel / drag, and track host element size
  // so the scale bar can show the on-screen length of N mm.

  type ViewBox = { x: number; y: number; w: number; h: number };

  let hostDiv: HTMLDivElement | undefined = $state();
  let svgEl: SVGSVGElement | null = null;
  let viewBox = $state<ViewBox | null>(null);
  let baseViewBox: ViewBox | null = null;
  let hostSize = $state<{ w: number; h: number }>({ w: 1, h: 1 });

  function parseViewBox(s: string): ViewBox | null {
    const parts = s.trim().split(/[\s,]+/).map(Number);
    if (parts.length !== 4 || parts.some(Number.isNaN)) return null;
    return { x: parts[0], y: parts[1], w: parts[2], h: parts[3] };
  }

  function applyViewBox(vb: ViewBox) {
    if (!svgEl) return;
    svgEl.setAttribute('viewBox', `${vb.x} ${vb.y} ${vb.w} ${vb.h}`);
    viewBox = vb;
  }

  // Convert a client (mouse) position to SVG user coords, accounting
  // for `preserveAspectRatio="xMidYMid meet"` letterboxing.
  function clientToSvg(clientX: number, clientY: number): { x: number; y: number } | null {
    if (!hostDiv || !viewBox) return null;
    const rect = hostDiv.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return null;
    const vb = viewBox;
    const scale = Math.min(rect.width / vb.w, rect.height / vb.h);
    const visW = rect.width / scale;
    const visH = rect.height / scale;
    const offX = vb.x - (visW - vb.w) / 2;
    const offY = vb.y - (visH - vb.h) / 2;
    return {
      x: offX + ((clientX - rect.left) / rect.width) * visW,
      y: offY + ((clientY - rect.top) / rect.height) * visH,
    };
  }

  function onWheel(e: WheelEvent) {
    if (!viewBox) return;
    e.preventDefault();
    const vb = viewBox;
    const factor = Math.exp(e.deltaY * 0.0015);
    const pt = clientToSvg(e.clientX, e.clientY);
    if (!pt) return;
    applyViewBox({
      x: pt.x - (pt.x - vb.x) * factor,
      y: pt.y - (pt.y - vb.y) * factor,
      w: vb.w * factor,
      h: vb.h * factor,
    });
  }

  let dragging = $state(false);
  let dragStart: { clientX: number; clientY: number; vbX: number; vbY: number } | null = null;

  function onPointerDown(e: PointerEvent) {
    if (!viewBox || e.button !== 0) return;
    dragging = true;
    dragStart = { clientX: e.clientX, clientY: e.clientY, vbX: viewBox.x, vbY: viewBox.y };
    (e.currentTarget as Element).setPointerCapture?.(e.pointerId);
  }

  function onPointerMove(e: PointerEvent) {
    if (!dragging || !dragStart || !viewBox || !hostDiv) return;
    const rect = hostDiv.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const vb = viewBox;
    // Use the same letterbox-aware scaling as clientToSvg so 1 client
    // pixel of drag maps to the same number of SVG units regardless of
    // which axis is letterboxed.
    const scale = Math.min(rect.width / vb.w, rect.height / vb.h);
    const dx = (e.clientX - dragStart.clientX) / scale;
    const dy = (e.clientY - dragStart.clientY) / scale;
    applyViewBox({ x: dragStart.vbX - dx, y: dragStart.vbY - dy, w: vb.w, h: vb.h });
  }

  function onPointerUp(e: PointerEvent) {
    dragging = false;
    dragStart = null;
    (e.currentTarget as Element).releasePointerCapture?.(e.pointerId);
  }

  function onDoubleClick() {
    if (baseViewBox) applyViewBox({ ...baseViewBox });
  }

  // Re-bind to the freshly-injected SVG whenever markup changes.
  $effect(() => {
    void svgMarkup;
    if (!hostDiv) return;
    untrack(() => {
      const found = hostDiv!.querySelector('svg') as SVGSVGElement | null;
      svgEl = found;
      if (!found) {
        viewBox = null;
        baseViewBox = null;
        return;
      }
      const vb = parseViewBox(found.getAttribute('viewBox') ?? '');
      if (vb) {
        baseViewBox = { ...vb };
        applyViewBox(vb);
      }
    });
  });

  // Track host size for the scale bar.
  $effect(() => {
    if (!hostDiv) return;
    const el = hostDiv;
    const update = () => {
      hostSize = { w: el.clientWidth, h: el.clientHeight };
    };
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  });

  // px-per-mm depends on the current viewBox and host size, using the
  // same `min` scaling as the SVG's preserveAspectRatio="xMidYMid meet".
  let pxPerMM = $derived.by(() => {
    if (!viewBox || hostSize.w <= 0 || hostSize.h <= 0) return 0;
    return Math.min(hostSize.w / viewBox.w, hostSize.h / viewBox.h);
  });

  // Pick a "nice" mm length (1/2/5 * 10^k) that renders 60–180 px wide.
  let scaleMM = $derived.by(() => {
    if (pxPerMM <= 0) return 1;
    const targetMM = 120 / pxPerMM;
    const exp = Math.floor(Math.log10(targetMM));
    const base = Math.pow(10, exp);
    const choices = [1 * base, 2 * base, 5 * base, 10 * base];
    let best = choices[0];
    let bestDiff = Math.abs(Math.log(choices[0] / targetMM));
    for (const c of choices) {
      const d = Math.abs(Math.log(c / targetMM));
      if (d < bestDiff) {
        best = c;
        bestDiff = d;
      }
    }
    return best;
  });

  function formatMM(v: number): string {
    if (v >= 1) return `${Number.isInteger(v) ? v : v.toFixed(1)} mm`;
    if (v >= 0.1) return `${v.toFixed(1)} mm`;
    return `${v.toFixed(2)} mm`;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-2xl max-h-[90vh] overflow-y-auto">
    <Dialog.Header>
      <Dialog.Title>Debug — Cells (sampled colors)</Dialog.Title>
      <Dialog.Description>
        Per-slab view of the cellslicer partition. Each cell is filled
        with its raw sampled RGB before dither.
        <span class="block text-xs text-muted-foreground mt-1">
          Drag to pan · scroll to zoom · double-click to reset
        </span>
      </Dialog.Description>
    </Dialog.Header>

    {#if errorMsg}
      <div class="text-sm text-red-500 p-3 rounded bg-red-50 dark:bg-red-950/30 whitespace-pre-wrap">
        {errorMsg}
      </div>
    {:else}
      <div class="flex items-center gap-3 text-sm">
        <label class="font-medium" for="dbg-slab-idx">Slab</label>
        <input
          id="dbg-slab-idx"
          type="range"
          min={0}
          max={Math.max(0, slabCount - 1)}
          bind:value={slabIdx}
          class="flex-1"
          disabled={slabCount === 0}
        />
        <span class="tabular-nums w-24 text-right text-muted-foreground">
          {slabIdx} / {Math.max(0, slabCount - 1)}
          {#if loading}
            <span aria-hidden="true">…</span>
          {/if}
        </span>
      </div>

      <div class="text-xs text-muted-foreground">
        Median cell area:
        <span class="tabular-nums font-medium text-foreground">
          {medianArea > 0 ? `${medianArea.toFixed(3)} mm²` : '—'}
        </span>
      </div>

      <div class="border border-border rounded bg-checkerboard relative overflow-hidden min-h-[300px]">
        <div
          bind:this={hostDiv}
          role="img"
          aria-label="Per-slab cell partition (pan, zoom, double-click to reset)"
          class="w-full max-h-[60vh] svg-host"
          class:grabbing={dragging}
          onwheel={onWheel}
          onpointerdown={onPointerDown}
          onpointermove={onPointerMove}
          onpointerup={onPointerUp}
          onpointercancel={onPointerUp}
          ondblclick={onDoubleClick}
        >
          {#if svgMarkup}
            {@html svgMarkup}
          {:else if loading}
            <div class="flex items-center justify-center h-[300px] text-sm text-muted-foreground">Loading…</div>
          {:else}
            <div class="flex items-center justify-center h-[300px] text-sm text-muted-foreground">No data</div>
          {/if}
        </div>

        {#if viewBox && pxPerMM > 0}
          <div class="scale-bar">
            <div class="scale-bar-line" style:width="{Math.max(1, scaleMM * pxPerMM)}px"></div>
            <span class="scale-bar-label">{formatMM(scaleMM)}</span>
          </div>
        {/if}
      </div>

      <div class="text-xs text-muted-foreground space-y-2 mt-1">
        <p class="font-medium text-foreground">What the colors mean</p>
        <p>
          The whole footprint is painted magenta first; everything else
          is drawn on top slightly transparent, so covered cells pick up
          a faint magenta wash and any cell-free area inside the
          footprint is flagged red.
        </p>
        <ul class="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1.5">
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#ff00ff"></span>
            <span><span class="font-medium text-foreground">Magenta</span> — footprint base fill: underlies the slab, showing as a faint wash on covered cells.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="linear-gradient(135deg,#c2410c,#0891b2,#16a34a)"></span>
            <span><span class="font-medium text-foreground">Cell fill</span> — each cell's raw sampled RGB, before dither.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#b4b4b4"></span>
            <span><span class="font-medium text-foreground">Grey cell</span> — no sample / transparent (alpha off): sampling found no surface color here.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#ff0000"></span>
            <span><span class="font-medium text-foreground">Red fill</span> — uncovered area: inside the footprint but reached by no cell (a coverage gap).</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="rgba(0,0,0,0.5)"></span>
            <span><span class="font-medium text-foreground">Thin grey lines</span> — each cell's outline (the partition grid), drawn translucent.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#e02020"></span>
            <span><span class="font-medium text-foreground">Thick red edges</span> — open outer-boundary edges that absorb geometry nudged past the outline.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#00b7eb"></span>
            <span><span class="font-medium text-foreground">Cyan</span> — raw bottom-Z slice contour for this slab.</span>
          </li>
          <li class="flex items-start gap-2">
            <span class="legend-swatch" style:background="#ff7f00"></span>
            <span><span class="font-medium text-foreground">Orange</span> — raw top-Z slice contour for this slab.</span>
          </li>
        </ul>
      </div>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<style>
  /* A simple grey/white checker so transparent/empty cells are visible. */
  :global(.bg-checkerboard) {
    background-image:
      linear-gradient(45deg, #e5e7eb 25%, transparent 25%),
      linear-gradient(-45deg, #e5e7eb 25%, transparent 25%),
      linear-gradient(45deg, transparent 75%, #e5e7eb 75%),
      linear-gradient(-45deg, transparent 75%, #e5e7eb 75%);
    background-size: 16px 16px;
    background-position: 0 0, 0 8px, 8px -8px, -8px 0;
  }
  /* Size the injected <svg> to fill the host while preserving aspect. */
  :global(.svg-host svg) {
    display: block;
    width: 100%;
    height: 100%;
    max-height: 60vh;
    touch-action: none;
  }
  .svg-host {
    cursor: grab;
    user-select: none;
    touch-action: none;
  }
  .svg-host.grabbing {
    cursor: grabbing;
  }
  .scale-bar {
    position: absolute;
    left: 12px;
    bottom: 12px;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    padding: 4px 6px;
    border-radius: 4px;
    background: rgba(255, 255, 255, 0.85);
    color: #111;
    font-size: 11px;
    line-height: 1;
    pointer-events: none;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.15);
  }
  .scale-bar-line {
    height: 3px;
    background: #111;
  }
  .scale-bar-label {
    font-variant-numeric: tabular-nums;
  }
  .legend-swatch {
    flex: 0 0 auto;
    width: 14px;
    height: 14px;
    margin-top: 1px;
    border-radius: 3px;
    border: 1px solid rgba(0, 0, 0, 0.2);
  }
</style>
