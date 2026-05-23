<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';

  type Pick = {
    viewerId: string;
    viewerLabel: string;
    faceIndex: number;
    vertices: [
      [number, number, number],
      [number, number, number],
      [number, number, number],
    ];
  };

  let {
    open = $bindable(false),
    pick = null,
  }: {
    open: boolean;
    pick: Pick | null;
  } = $props();

  function fmt(n: number): string {
    return n.toFixed(4);
  }

  function fmtVec(v: [number, number, number]): string {
    return `${fmt(v[0])}, ${fmt(v[1])}, ${fmt(v[2])}`;
  }

  function centroid(p: Pick): [number, number, number] {
    const [a, b, c] = p.vertices;
    return [(a[0] + b[0] + c[0]) / 3, (a[1] + b[1] + c[1]) / 3, (a[2] + b[2] + c[2]) / 3];
  }

  function asText(p: Pick): string {
    const c = centroid(p);
    return [
      `Source:      ${p.viewerLabel}`,
      `Face index:  ${p.faceIndex}`,
      `Vertex 0:    ${fmtVec(p.vertices[0])}`,
      `Vertex 1:    ${fmtVec(p.vertices[1])}`,
      `Vertex 2:    ${fmtVec(p.vertices[2])}`,
      `Centroid:    ${fmtVec(c)}`,
    ].join('\n');
  }

  let copied = $state(false);
  async function copyAll() {
    if (!pick) return;
    try {
      await navigator.clipboard.writeText(asText(pick));
      copied = true;
      setTimeout(() => { copied = false; }, 1500);
    } catch {
      // ignore — user can still select-and-copy from the displayed text.
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>Selected Triangle</Dialog.Title>
      <Dialog.Description>
        World-space vertex coordinates of the picked face. Mesh coordinates are in mm (after pipeline scaling).
      </Dialog.Description>
    </Dialog.Header>
    {#if pick}
      <pre class="text-xs font-mono whitespace-pre rounded border border-border bg-muted/40 p-3 overflow-x-auto select-text">{asText(pick)}</pre>
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
