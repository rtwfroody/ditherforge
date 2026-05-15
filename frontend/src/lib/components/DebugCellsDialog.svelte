<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import { DebugCellsSlabSVG } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let {
    open = $bindable(false),
  }: {
    open: boolean;
  } = $props();

  let slabIdx = $state(0);
  let slabCount = $state(0);
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
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-2xl max-h-[90vh] overflow-y-auto">
    <Dialog.Header>
      <Dialog.Title>Debug — Cells (sampled colors)</Dialog.Title>
      <Dialog.Description>
        Per-slab view of the cellslicer partition. Each cell is filled
        with its raw sampled RGB before dither.
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

      <div class="border border-border rounded bg-checkerboard flex items-center justify-center min-h-[300px] overflow-hidden">
        {#if svgMarkup}
          <div class="w-full max-h-[60vh] svg-host">
            {@html svgMarkup}
          </div>
        {:else if loading}
          <span class="text-sm text-muted-foreground">Loading…</span>
        {:else}
          <span class="text-sm text-muted-foreground">No data</span>
        {/if}
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
    height: auto;
    max-height: 60vh;
  }
</style>
