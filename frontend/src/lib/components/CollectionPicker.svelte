<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import * as Select from '$lib/components/ui/select';
  import { contrastColor } from '$lib/utils';
  import { collectionStore } from '$lib/stores/collections.svelte';

  let {
    onselect,
    onclose,
  }: {
    onselect: (hex: string) => void;
    onclose: () => void;
  } = $props();

  collectionStore.ensureLoaded();
</script>

<div class="border rounded-lg bg-popover p-3 shadow-md space-y-2 w-full">
  <div class="flex items-center gap-2">
    <Select.Root type="single" value={collectionStore.activeCollection} onValueChange={(v: string) => collectionStore.select(v)}>
      <Select.Trigger class="flex-1">
        {#if collectionStore.activeCollection}{collectionStore.activeCollection} ({collectionStore.collections.find(c => c.name === collectionStore.activeCollection)?.count ?? 0}){:else}Select collection...{/if}
      </Select.Trigger>
      <Select.Content>
        {#each collectionStore.collections as col}
          <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
        {/each}
      </Select.Content>
    </Select.Root>
    <Button variant="ghost" size="sm" onclick={onclose}>Close</Button>
  </div>
  <div class="grid grid-cols-4 gap-1 max-h-48 overflow-y-auto">
    {#each collectionStore.colors as color}
      <button
        type="button"
        class="h-8 rounded border cursor-pointer flex items-center justify-center text-[10px] leading-tight select-none hover:ring-2 hover:ring-primary transition-shadow"
        style="background: {color.hex}; color: {contrastColor(color.hex)};"
        title="{color.label || color.hex}"
        onclick={() => onselect(color.hex)}
      >
        {color.label || color.hex}
      </button>
    {/each}
  </div>
</div>
