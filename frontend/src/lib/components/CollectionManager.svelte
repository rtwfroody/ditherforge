<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import * as Card from '$lib/components/ui/card';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import { Separator } from '$lib/components/ui/separator';
  import { contrastColor } from '$lib/utils';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { ImportCollection, DeleteCollection } from '../../../wailsjs/go/main/App';

  let deleteTarget = $state('');
  let deleteDialogOpen = $state(false);

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
    await DeleteCollection(deleteTarget);
    if (collectionStore.activeCollection === deleteTarget) {
      collectionStore.activeCollection = '';
      collectionStore.colors = [];
    }
    deleteDialogOpen = false;
    deleteTarget = '';
    await collectionStore.refresh();
  }

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
            {#if !col.builtIn}
              <Button
                variant="ghost"
                size="sm"
                class="text-destructive hover:text-destructive h-7 px-2"
                onclick={(e: MouseEvent) => { e.stopPropagation(); confirmDelete(col.name); }}
              >Delete</Button>
            {:else}
              <span class="text-xs text-muted-foreground">built-in</span>
            {/if}
          </button>
        {/each}
      </div>

      <Button variant="outline" size="sm" onclick={handleImport}>Import from file...</Button>

      <Separator />

      <!-- Color swatches for active collection -->
      {#if collectionStore.colors.length > 0}
        <div class="space-y-2">
          <span class="text-sm font-medium">{collectionStore.activeCollection}</span>
          <div class="grid grid-cols-4 gap-2">
            {#each collectionStore.colors as color}
              <div
                class="h-10 rounded border flex items-center justify-center text-[10px] leading-tight select-none"
                style="background: {color.hex}; color: {contrastColor(color.hex)};"
                title="{color.hex}"
              >
                {color.label || color.hex}
              </div>
            {/each}
          </div>
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
