<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import { DebugCellsSlabPNG } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let {
    open = $bindable(false),
  }: {
    open: boolean;
  } = $props();

  let slabIdx = $state(0);
  let slabCount = $state(0);
  let pngSrc = $state<string>('');
  let errorMsg = $state<string>('');
  let loading = $state(false);

  // Reset slab idx and fetch the first frame whenever the dialog
  // opens. Keep the prior slabIdx within bounds otherwise.
  let prevOpen = false;
  $effect(() => {
    if (open && !prevOpen) {
      slabIdx = 0;
      refresh();
    }
    prevOpen = open;
  });

  // Re-fetch whenever the slab index changes while the dialog is open.
  let prevIdx = -1;
  $effect(() => {
    if (open && slabIdx !== prevIdx) {
      prevIdx = slabIdx;
      refresh();
    }
  });

  async function refresh() {
    loading = true;
    errorMsg = '';
    try {
      const r = (await DebugCellsSlabPNG(slabIdx)) as main.DebugCellsSlabResult;
      slabCount = r.slabCount;
      pngSrc = 'data:image/png;base64,' + r.pngBase64;
    } catch (e: any) {
      errorMsg = e?.toString?.() ?? String(e);
      pngSrc = '';
    } finally {
      loading = false;
    }
  }
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
          disabled={slabCount === 0 || loading}
        />
        <span class="tabular-nums w-24 text-right text-muted-foreground">
          {slabIdx} / {Math.max(0, slabCount - 1)}
        </span>
      </div>

      <div class="border border-border rounded bg-checkerboard flex items-center justify-center min-h-[300px]">
        {#if pngSrc}
          <img src={pngSrc} alt="Slab {slabIdx} cells" class="max-w-full max-h-[60vh] object-contain" />
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
</style>
