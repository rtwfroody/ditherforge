<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Select from '$lib/components/ui/select';
  import { contrastColor } from '$lib/utils';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { SaveCollectionColors, ResolveColor, GetCollectionColors } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  // Color editing state
  let colorInput = $state('');
  let colorInputError = $state('');
  let pickFromCollection = $state(false);
  let pickSourceCollection = $state('');
  let pickSourceColors = $state<main.ColorEntry[]>([]);

  // Inline swatch editing state
  let editIndex = $state(-1);
  let editHex = $state('');
  let editLabel = $state('');
  let editTD = $state(1);
  let editError = $state('');

  // Default transmission distance (mm) for a newly added filament; matches
  // the backend's opaque DefaultTD so new colors don't change dither output
  // until the user gives them a real TD.
  const DEFAULT_TD = 1;

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

  async function addColorFromPicker(hex: string, label: string, td: number) {
    if (!collectionStore.activeCollection) return;
    const entry: main.ColorEntry = { hex, label, td: td || DEFAULT_TD } as main.ColorEntry;
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
    if (editIndex >= 0) {
      if (index === editIndex) editIndex = -1;
      else if (index < editIndex) editIndex--;
    }
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

  function startEdit(index: number) {
    const color = collectionStore.colors[index];
    editIndex = index;
    editHex = color.hex;
    editLabel = color.label;
    editTD = color.td || DEFAULT_TD;
    editError = '';
  }

  function cancelEdit() {
    editIndex = -1;
    editError = '';
  }

  async function saveEdit() {
    if (editIndex < 0 || !collectionStore.activeCollection) return;
    editError = '';
    try {
      const resolved = await ResolveColor(editHex.trim());
      const newColors = [...collectionStore.colors];
      const td = Number(editTD) > 0 ? Number(editTD) : DEFAULT_TD;
      newColors[editIndex] = { hex: resolved.hex, label: editLabel.trim() || resolved.label, td } as main.ColorEntry;
      await SaveCollectionColors(collectionStore.activeCollection, newColors);
      collectionStore.setColors(newColors);
      editIndex = -1;
    } catch (err: any) {
      editError = String(err);
    }
  }

  function handleColorInputKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      addColor();
    }
  }

  function handleEditKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter') saveEdit();
    if (e.key === 'Escape') cancelEdit();
  }

  // Whether the active collection is editable (not built-in).
  let isEditable = $derived(collectionStore.isEditable);

  // Reset picker/edit state when active collection changes.
  $effect(() => {
    void collectionStore.activeCollection;
    pickFromCollection = false;
    pickSourceCollection = '';
    pickSourceColors = [];
    colorInput = '';
    colorInputError = '';
    editIndex = -1;
    editError = '';
  });

</script>

<div class="space-y-4">
      <!-- Color swatches for active collection -->
      {#if collectionStore.activeCollection}
        <div class="space-y-2">
          {#if !isEditable}
            <p class="text-sm text-muted-foreground">Built-in collection (read-only)</p>
          {/if}
          <div class="grid grid-cols-6 gap-2">
            {#each collectionStore.colors as color, i}
              <div class="group relative">
                {#if isEditable}
                  <button
                    type="button"
                    class="w-full h-13 rounded border flex items-center justify-center text-[10px] leading-tight select-none text-center px-1 cursor-pointer hover:ring-2 hover:ring-primary transition-shadow"
                    style="background: {color.hex}; color: {contrastColor(color.hex)};"
                    title="{color.hex}{color.label ? ' — ' + color.label : ''}{color.td ? ' · TD ' + color.td : ''}"
                    onclick={() => startEdit(i)}
                  >
                    {#if color.label}{color.label}<br>{/if}{color.hex}
                  </button>
                {:else}
                  <div
                    class="h-13 rounded border flex items-center justify-center text-[10px] leading-tight select-none text-center px-1"
                    style="background: {color.hex}; color: {contrastColor(color.hex)};"
                    title="{color.hex}{color.label ? ' — ' + color.label : ''}{color.td ? ' · TD ' + color.td : ''}"
                  >
                    {#if color.label}{color.label}<br>{/if}{color.hex}
                  </div>
                {/if}
                {#if isEditable}
                  <button
                    class="absolute -top-1 -right-1 w-4 h-4 rounded-full bg-destructive text-destructive-foreground text-xs leading-none opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center cursor-pointer"
                    onclick={(e: MouseEvent) => { e.stopPropagation(); removeColor(i); }}
                  >&times;</button>
                {/if}
              </div>
            {/each}
          </div>

          <!-- Inline edit panel -->
          {#if editIndex >= 0 && isEditable}
            <div class="border rounded-lg bg-popover p-3 space-y-2">
              <div class="flex items-center gap-2">
                <div class="w-8 h-8 rounded border shrink-0" style="background: {editHex || collectionStore.colors[editIndex]?.hex};"></div>
                <span class="text-sm font-medium">Edit color</span>
              </div>
              <div class="flex gap-2">
                <Input
                  placeholder="Hex (#FF0000) or name"
                  bind:value={editHex}
                  onkeydown={handleEditKeydown}
                  class="flex-1"
                />
              </div>
              <div class="flex gap-2">
                <Input
                  placeholder="Label (optional)"
                  bind:value={editLabel}
                  onkeydown={handleEditKeydown}
                  class="flex-1"
                />
              </div>
              <div class="flex items-center gap-2">
                <label class="text-xs text-muted-foreground whitespace-nowrap" for="edit-td">TD (mm)</label>
                <Input
                  id="edit-td"
                  type="number"
                  min="0.1"
                  step="0.1"
                  bind:value={editTD}
                  onkeydown={handleEditKeydown}
                  class="flex-1"
                />
              </div>
              <p class="text-[10px] text-muted-foreground leading-snug">
                Transmission distance: higher = more translucent (light passes through, mixes less).
                ~1 = opaque, ~4+ = see-through. Affects how the color dithers.
              </p>
              {#if editError}
                <p class="text-xs text-destructive">{editError}</p>
              {/if}
              <div class="flex gap-2 justify-end">
                <Button variant="ghost" size="sm" onclick={cancelEdit}>Cancel</Button>
                <Button variant="outline" size="sm" onclick={saveEdit}>Save</Button>
              </div>
            </div>
          {/if}

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
                    <div class="grid grid-cols-6 gap-1 max-h-48 overflow-y-auto">
                      {#each pickSourceColors as color}
                        <button
                          type="button"
                          class="h-13 rounded border cursor-pointer flex items-center justify-center text-[10px] leading-tight select-none text-center px-1 hover:ring-2 hover:ring-primary transition-shadow"
                          style="background: {color.hex}; color: {contrastColor(color.hex)};"
                          title="{color.hex}{color.label ? ' — ' + color.label : ''}{color.td ? ' · TD ' + color.td : ''}"
                          onclick={() => addColorFromPicker(color.hex, color.label, color.td)}
                        >
                          {#if color.label}{color.label}<br>{/if}{color.hex}
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
</div>

