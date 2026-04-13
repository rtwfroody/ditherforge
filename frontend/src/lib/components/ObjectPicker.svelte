<script lang="ts">
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import type { loader } from '../../../wailsjs/go/models';

  let {
    objects = [],
    open = $bindable(false),
    onSelect,
  }: {
    objects: loader.ObjectInfo[];
    open: boolean;
    onSelect: (index: number) => void;
  } = $props();

  function formatTriCount(n: number): string {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
    return String(n);
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-xl max-h-[80vh] overflow-y-auto">
    <Dialog.Header>
      <Dialog.Title>Select Object</Dialog.Title>
      <Dialog.Description>This file contains {objects.length} objects. Choose one to load.</Dialog.Description>
    </Dialog.Header>

    <div class="grid grid-cols-2 gap-3">
      <button
        class="flex flex-col items-center gap-1.5 p-3 rounded-lg border border-dashed border-muted-foreground/30 hover:bg-accent hover:border-accent-foreground/20 cursor-pointer transition-colors"
        onclick={() => { onSelect(-1); open = false; }}
      >
        <div class="w-full aspect-square bg-muted rounded flex items-center justify-center text-xs text-muted-foreground">
          All
        </div>
        <span class="text-xs font-medium">All Objects</span>
        <span class="text-[10px] text-muted-foreground">
          {formatTriCount(objects.reduce((sum, o) => sum + o.triCount, 0))} triangles
        </span>
      </button>

      {#each objects as obj}
        <button
          class="flex flex-col items-center gap-1.5 p-3 rounded-lg border border-border hover:bg-accent hover:border-accent-foreground/20 cursor-pointer transition-colors"
          onclick={() => { onSelect(obj.index); open = false; }}
        >
          {#if obj.thumbnail}
            <img src={obj.thumbnail} alt={obj.name} class="w-full aspect-square rounded object-contain bg-muted" />
          {:else}
            <div class="w-full aspect-square bg-muted rounded flex items-center justify-center text-xs text-muted-foreground">
              No preview
            </div>
          {/if}
          <span class="text-xs font-medium truncate w-full text-center">{obj.name}</span>
          <span class="text-[10px] text-muted-foreground">{formatTriCount(obj.triCount)} triangles</span>
        </button>
      {/each}
    </div>
  </Dialog.Content>
</Dialog.Root>
