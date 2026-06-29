<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import type { pipeline } from '../../../wailsjs/go/models';

  let {
    open = $bindable(false),
    info = null,
    error = '',
    loading = false,
  }: {
    open: boolean;
    info: pipeline.CellDiagInfo | null;
    error?: string;
    loading?: boolean;
  } = $props();

  function fmt(n: number): string {
    return Number.isFinite(n) ? n.toFixed(4) : String(n);
  }
  function fmtVec(v: number[] | undefined): string {
    if (!v) return '—';
    return v.map(fmt).join(', ');
  }
  function rgb(c: number[] | undefined): string {
    if (!c) return '—';
    return `${c[0]}, ${c[1]}, ${c[2]}`;
  }
  function hex(c: number[] | undefined): string {
    if (!c) return '';
    const h = (n: number) => Math.max(0, Math.min(255, Math.round(n))).toString(16).padStart(2, '0');
    return `#${h(c[0])}${h(c[1])}${h(c[2])}`;
  }

  function asText(i: pipeline.CellDiagInfo): string {
    const lines: string[] = [];
    if (!i.found) {
      lines.push('No cell found under the click.');
      lines.push(`Pick point (preview): ${fmtVec(i.pickPointPreview)}`);
      lines.push(`Pick point (cell):    ${fmtVec(i.pickPointCell)}`);
      lines.push(`Preview scale:        ${fmt(i.previewScale)}`);
      return lines.join('\n');
    }
    lines.push(`Cell:            slab ${i.slabIdx}, cell ${i.cellIdx}${i.split ? `, half ${i.halfIdx}` : ''}`);
    if (i.matchedByNearest) lines.push(`(matched by nearest centroid — click was just outside every cell polygon)`);
    lines.push(`Slab Z range:    ${fmt(i.slabZBot)} … ${fmt(i.slabZTop)} mm`);
    lines.push(`Cell area:       ${fmt(i.areaMM2)} mm²`);
    lines.push(`Surface normal:  ${fmtVec(i.normal)}`);
    lines.push(`Centroid:        ${fmtVec(i.centroid)}`);
    lines.push('');
    lines.push(`Stored color:    ${rgb(i.storedColor)}  ${hex(i.storedColor)}  (alpha ${i.storedAlpha})`);
    lines.push(`Recomputed:      ${rgb(i.recomputedColor)}  ${hex(i.recomputedColor)}  (alpha ${i.recomputedAlpha})`);
    lines.push('');
    lines.push(`Ray start-back:  ${fmt(i.startBack)} mm   reach: ${fmt(i.reach)} mm`);
    lines.push(`Pick point (preview): ${fmtVec(i.pickPointPreview)}`);
    lines.push(`Pick point (cell):    ${fmtVec(i.pickPointCell)}   (preview scale ${fmt(i.previewScale)})`);
    lines.push('');
    lines.push(`Sample rays (${i.rays?.length ?? 0}) — each casts from origin along dir = -normal:`);
    (i.rays ?? []).forEach((r, idx) => {
      const status = r.fallback ? 'FALLBACK(nearest-face)' : r.hit ? 'hit' : (r.bvhUsed ? 'miss' : 'no-bvh');
      lines.push(`  [${idx}] ${status}${r.counted ? '' : ' (not counted, alpha<128)'}`);
      lines.push(`      sample point:  ${fmtVec(r.point)}`);
      if (i.split) lines.push(`      color-frame pt:${fmtVec(r.colorPoint)}`);
      lines.push(`      ray origin:    ${fmtVec(r.origin)}`);
      lines.push(`      ray dir:       ${fmtVec(r.dir)}   maxT ${fmt(r.maxT)}`);
      if (r.hit && !r.fallback) {
        lines.push(`      hit tri:       ${r.hitTri}   t=${fmt(r.hitT)}`);
        lines.push(`      hit point:     ${fmtVec(r.hitPoint)}`);
      }
      lines.push(`      color:         ${rgb(r.color?.slice(0, 3))}  ${hex(r.color)}  alpha ${r.color?.[3] ?? '—'}`);
    });
    return lines.join('\n');
  }

  let copied = $state(false);
  async function copyAll() {
    if (!info) return;
    try {
      await navigator.clipboard.writeText(asText(info));
      copied = true;
      setTimeout(() => { copied = false; }, 1500);
    } catch {
      // ignore — user can still select-and-copy from the displayed text.
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-2xl">
    <Dialog.Header>
      <Dialog.Title>Selected Cell</Dialog.Title>
      <Dialog.Description>
        Color-sampling diagnostics for the clicked cell: surface normal and every along-normal sample ray (origin, direction, hit, color). Recomputed from the cached Voxelize partition.
      </Dialog.Description>
    </Dialog.Header>
    {#if loading}
      <div class="text-sm text-muted-foreground p-3">Resolving cell…</div>
    {:else if error}
      <div class="text-sm text-red-400 p-3 whitespace-pre-wrap">{error}</div>
    {:else if info}
      {#if info.found}
        <div class="flex items-center gap-3 text-xs">
          <span>Stored</span>
          <span class="inline-block w-6 h-6 rounded border border-border" style="background:{hex(info.storedColor)}"></span>
          <span>Recomputed</span>
          <span class="inline-block w-6 h-6 rounded border border-border" style="background:{hex(info.recomputedColor)}"></span>
        </div>
      {/if}
      <pre class="text-xs font-mono whitespace-pre rounded border border-border bg-muted/40 p-3 overflow-auto max-h-[55vh] select-text">{asText(info)}</pre>
      <Dialog.Footer>
        <button
          type="button"
          class="h-9 px-3 rounded border border-border bg-background hover:bg-muted text-sm"
          onclick={copyAll}
        >{copied ? 'Copied!' : 'Copy'}</button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>
