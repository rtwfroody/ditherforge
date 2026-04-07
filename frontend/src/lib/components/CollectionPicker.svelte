<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import * as Select from '$lib/components/ui/select';
  import { ListCollections, GetCollectionColors, ImportCollection, DeleteCollection } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let {
    onselect,
    onclose,
  }: {
    onselect: (hex: string) => void;
    onclose: () => void;
  } = $props();

  let collections = $state<main.CollectionInfo[]>([]);
  let activeCollection = $state('');
  let colors = $state<main.ColorEntry[]>([]);
  let manageMode = $state(false);

  async function loadCollections() {
    collections = (await ListCollections()) ?? [];
    if (collections.length > 0 && !activeCollection) {
      activeCollection = collections[0].name;
    }
    if (activeCollection) {
      await loadColors(activeCollection);
    }
  }

  async function loadColors(name: string) {
    colors = (await GetCollectionColors(name)) ?? [];
  }

  function selectCollection(name: string) {
    activeCollection = name;
    loadColors(name);
  }

  function pickColor(hex: string) {
    onselect(hex);
  }

  async function handleImport() {
    const name = await ImportCollection();
    if (name) {
      await loadCollections();
      activeCollection = name;
      await loadColors(name);
    }
  }

  async function handleDelete(name: string) {
    await DeleteCollection(name);
    await loadCollections();
  }

  // Contrast helper: return white or black text based on background.
  function contrastColor(hex: string): string {
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return (r * 0.299 + g * 0.587 + b * 0.114) > 140 ? '#000' : '#fff';
  }

  // Load on mount.
  loadCollections();
</script>

<div class="border rounded-lg bg-popover p-3 shadow-md space-y-2 w-full">
  {#if manageMode}
    <!-- Manage collections view -->
    <div class="flex items-center justify-between mb-2">
      <span class="text-sm font-medium">Manage Collections</span>
      <Button variant="ghost" size="sm" onclick={() => { manageMode = false; }}>Back</Button>
    </div>
    <div class="space-y-1 max-h-48 overflow-y-auto">
      {#each collections as col}
        <div class="flex items-center justify-between text-sm py-1 px-2 rounded hover:bg-muted">
          <span>{col.name} <span class="text-muted-foreground">({col.count})</span></span>
          {#if !col.builtIn}
            <button
              class="text-destructive text-xs hover:underline"
              onclick={() => handleDelete(col.name)}
            >Delete</button>
          {:else}
            <span class="text-xs text-muted-foreground">built-in</span>
          {/if}
        </div>
      {/each}
    </div>
    <Button variant="outline" size="sm" class="w-full" onclick={handleImport}>Import from file...</Button>
  {:else}
    <!-- Picker view -->
    <div class="flex items-center gap-2">
      <Select.Root type="single" value={activeCollection} onValueChange={(v: string) => selectCollection(v)}>
        <Select.Trigger class="flex-1">
          {#if activeCollection}{activeCollection} ({collections.find(c => c.name === activeCollection)?.count ?? 0}){:else}Select collection...{/if}
        </Select.Trigger>
        <Select.Content>
          {#each collections as col}
            <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
          {/each}
        </Select.Content>
      </Select.Root>
      <Button variant="ghost" size="sm" onclick={() => { manageMode = true; }}>Manage</Button>
      <Button variant="ghost" size="sm" onclick={onclose}>Close</Button>
    </div>
    <div class="grid grid-cols-4 gap-1 max-h-48 overflow-y-auto">
      {#each colors as color}
        <button
          type="button"
          class="h-8 rounded border cursor-pointer flex items-center justify-center text-[10px] leading-tight select-none hover:ring-2 hover:ring-primary transition-shadow"
          style="background: {color.hex}; color: {contrastColor(color.hex)};"
          title="{color.label || color.hex}"
          onclick={() => pickColor(color.hex)}
        >
          {color.label || color.hex}
        </button>
      {/each}
    </div>
  {/if}
</div>
