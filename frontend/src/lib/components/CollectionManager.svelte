<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Card from '$lib/components/ui/card';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import * as Select from '$lib/components/ui/select';
  import { Separator } from '$lib/components/ui/separator';
  import { contrastColor } from '$lib/utils';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { ImportCollection, DeleteCollection, CreateCollection, SaveCollectionColors, ResolveColor, GetCollectionColors } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let deleteTarget = $state('');
  let deleteDialogOpen = $state(false);
  let newCollectionName = $state('');
  let newCollectionDialogOpen = $state(false);

  // Color editing state
  let colorInput = $state('');
  let colorInputError = $state('');
  let pickFromCollection = $state(false);
  let pickSourceCollection = $state('');
  let pickSourceColors = $state<main.ColorEntry[]>([]);

  async function handleImport() {
    const name = await ImportCollection();
    if (name) {
      await collectionStore.refresh();
      collectionStore.select(name);
    }
  }

  function confirmDelete(name: string) {
    deleteTarget = name;
    deleteDialogOpen = true;
  }

  async function handleDelete() {
    try {
      await DeleteCollection(deleteTarget);
      if (collectionStore.activeCollection === deleteTarget) {
        collectionStore.activeCollection = '';
        collectionStore.colors = [];
      }
      deleteDialogOpen = false;
      deleteTarget = '';
      await collectionStore.refresh();
    } catch (err) {
      console.error('Failed to delete collection:', err);
      deleteDialogOpen = false;
    }
  }

  function openNewCollectionDialog() {
    newCollectionName = '';
    newCollectionDialogOpen = true;
  }

  async function handleCreateCollection() {
    if (!newCollectionName.trim()) return;
    try {
      await CreateCollection(newCollectionName.trim());
      newCollectionDialogOpen = false;
      await collectionStore.refresh();
      collectionStore.select(newCollectionName.trim());
    } catch (err) {
      // Name conflict or other error — could show error but keep it simple
      console.error('Failed to create collection:', err);
    }
  }

  async function addColor() {
    if (!colorInput.trim() || !collectionStore.activeCollection) return;
    colorInputError = '';
    try {
      const resolved = await ResolveColor(colorInput.trim());
      const newColors = [...collectionStore.colors, resolved];
      await SaveCollectionColors(collectionStore.activeCollection, newColors);
      collectionStore.setColors(newColors);
      colorInput = '';
    } catch (err: any) {
      colorInputError = String(err);
    }
  }

  async function addColorFromPicker(hex: string, label: string) {
    if (!collectionStore.activeCollection) return;
    const entry: main.ColorEntry = { hex, label } as main.ColorEntry;
    const newColors = [...collectionStore.colors, entry];
    try {
      await SaveCollectionColors(collectionStore.activeCollection, newColors);
      collectionStore.setColors(newColors);
    } catch (err) {
      console.error('Failed to add color:', err);
    }
  }

  async function removeColor(index: number) {
    if (!collectionStore.activeCollection) return;
    const newColors = collectionStore.colors.filter((_, i) => i !== index);
    try {
      await SaveCollectionColors(collectionStore.activeCollection, newColors);
      collectionStore.setColors(newColors);
    } catch (err) {
      console.error('Failed to remove color:', err);
    }
  }

  async function loadPickSourceColors(name: string) {
    pickSourceCollection = name;
    if (!name) {
      pickSourceColors = [];
      return;
    }
    pickSourceColors = (await GetCollectionColors(name)) ?? [];
  }

  function handleColorInputKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      addColor();
    }
  }

  // Whether the active collection is editable (not built-in).
  let isEditable = $derived(
    collectionStore.activeCollection &&
    !collectionStore.collections.find(c => c.name === collectionStore.activeCollection)?.builtIn
  );

  // Reset picker state when active collection changes.
  $effect(() => {
    void collectionStore.activeCollection;
    pickFromCollection = false;
    pickSourceCollection = '';
    pickSourceColors = [];
    colorInput = '';
    colorInputError = '';
  });

  collectionStore.ensureLoaded();
</script>

<div class="h-full flex flex-col p-6 overflow-y-auto">
  <h2 class="text-xl font-bold mb-4">Filament Collections</h2>

  <Card.Root>
    <Card.Content class="pt-6 space-y-4">
      <!-- Collection list -->
      <div class="space-y-1">
        {#each collectionStore.collections as col}
          <button
            type="button"
            class="w-full flex items-center justify-between text-sm py-2 px-3 rounded cursor-pointer transition-colors text-left {col.name === collectionStore.activeCollection ? 'bg-muted' : 'hover:bg-muted/50'}"
            onclick={() => collectionStore.select(col.name)}
          >
            <span class="font-medium">{col.name} <span class="text-muted-foreground font-normal">({col.count})</span></span>
            {#if col.builtIn}
              <span class="text-xs text-muted-foreground">built-in</span>
            {:else if col.name === 'Inventory'}
              <span class="text-xs text-muted-foreground">default</span>
            {:else}
              <Button
                variant="ghost"
                size="sm"
                class="text-destructive hover:text-destructive h-7 px-2"
                onclick={(e: MouseEvent) => { e.stopPropagation(); confirmDelete(col.name); }}
              >Delete</Button>
            {/if}
          </button>
        {/each}
      </div>

      <div class="flex gap-2">
        <Button variant="outline" size="sm" onclick={openNewCollectionDialog}>New collection</Button>
        <Button variant="outline" size="sm" onclick={handleImport}>Import from file...</Button>
      </div>

      <Separator />

      <!-- Color swatches for active collection -->
      {#if collectionStore.activeCollection}
        <div class="space-y-2">
          <span class="text-sm font-medium">{collectionStore.activeCollection}</span>
          <div class="grid grid-cols-4 gap-2">
            {#each collectionStore.colors as color, i}
              <div class="group relative">
                <div
                  class="h-10 rounded border flex items-center justify-center text-[10px] leading-tight select-none"
                  style="background: {color.hex}; color: {contrastColor(color.hex)};"
                  title="{color.label || color.hex}"
                >
                  {color.label || color.hex}
                </div>
                {#if isEditable}
                  <button
                    class="absolute -top-1 -right-1 w-4 h-4 rounded-full bg-destructive text-destructive-foreground text-xs leading-none opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center cursor-pointer"
                    onmousedown={(e: MouseEvent) => { e.stopPropagation(); removeColor(i); }}
                  >&times;</button>
                {/if}
              </div>
            {/each}
          </div>

          <!-- Add color (only for editable collections) -->
          {#if isEditable}
            <div class="space-y-2 pt-2">
              <div class="flex gap-2">
                <Input
                  placeholder="Add color: hex (#FF0000) or name (red)"
                  bind:value={colorInput}
                  onkeydown={handleColorInputKeydown}
                  class="flex-1"
                />
                <Button variant="outline" size="sm" onclick={addColor} disabled={!colorInput.trim()}>Add</Button>
                <Button variant="outline" size="sm" onclick={() => { pickFromCollection = !pickFromCollection; }}>
                  {pickFromCollection ? 'Close' : 'Pick...'}
                </Button>
              </div>
              {#if colorInputError}
                <p class="text-xs text-destructive">{colorInputError}</p>
              {/if}

              <!-- Pick from another collection -->
              {#if pickFromCollection}
                <div class="border rounded-lg bg-popover p-3 space-y-2">
                  <Select.Root type="single" value={pickSourceCollection} onValueChange={(v: string) => loadPickSourceColors(v)}>
                    <Select.Trigger class="flex-1">
                      {#if pickSourceCollection}{pickSourceCollection}{:else}Pick from collection...{/if}
                    </Select.Trigger>
                    <Select.Content>
                      {#each collectionStore.collections.filter(c => c.name !== collectionStore.activeCollection) as col}
                        <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
                      {/each}
                    </Select.Content>
                  </Select.Root>
                  {#if pickSourceColors.length > 0}
                    <div class="grid grid-cols-4 gap-1 max-h-48 overflow-y-auto">
                      {#each pickSourceColors as color}
                        <button
                          type="button"
                          class="h-8 rounded border cursor-pointer flex items-center justify-center text-[10px] leading-tight select-none hover:ring-2 hover:ring-primary transition-shadow"
                          style="background: {color.hex}; color: {contrastColor(color.hex)};"
                          title="{color.label || color.hex}"
                          onclick={() => addColorFromPicker(color.hex, color.label)}
                        >
                          {color.label || color.hex}
                        </button>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    </Card.Content>
  </Card.Root>
</div>

<AlertDialog.Root bind:open={deleteDialogOpen}>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title>Delete collection</AlertDialog.Title>
      <AlertDialog.Description>
        Are you sure you want to delete "{deleteTarget}"? This cannot be undone.
      </AlertDialog.Description>
    </AlertDialog.Header>
    <AlertDialog.Footer>
      <AlertDialog.Cancel>Cancel</AlertDialog.Cancel>
      <AlertDialog.Action onclick={handleDelete}>Delete</AlertDialog.Action>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>

<AlertDialog.Root bind:open={newCollectionDialogOpen}>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title>New collection</AlertDialog.Title>
      <AlertDialog.Description>
        Enter a name for the new collection.
      </AlertDialog.Description>
    </AlertDialog.Header>
    <div class="py-4">
      <Input
        bind:value={newCollectionName}
        placeholder="Collection name"
        onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter') handleCreateCollection(); }}
      />
    </div>
    <AlertDialog.Footer>
      <AlertDialog.Cancel>Cancel</AlertDialog.Cancel>
      <AlertDialog.Action onclick={handleCreateCollection} disabled={!newCollectionName.trim()}>Create</AlertDialog.Action>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
