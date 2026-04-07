<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import * as Select from '$lib/components/ui/select';
  import { ListCollections, ImportCollection } from '../../../wailsjs/go/main/App';
  import type { main } from '../../../wailsjs/go/models';

  let {
    selected = $bindable(''),
    onchange,
  }: {
    selected: string;
    onchange: (name: string) => void;
  } = $props();

  let collections = $state<main.CollectionInfo[]>([]);

  async function load() {
    collections = (await ListCollections()) ?? [];
    if (collections.length > 0 && !selected) {
      selected = collections[0].name;
      onchange(selected);
    }
  }

  function handleChange(v: string) {
    selected = v;
    onchange(selected);
  }

  async function handleImport() {
    const name = await ImportCollection();
    if (name) {
      await load();
      selected = name;
      onchange(selected);
    }
  }

  load();
</script>

<div class="flex gap-2">
  <Select.Root type="single" value={selected} onValueChange={handleChange}>
    <Select.Trigger class="flex-1">
      {#if selected}{selected} ({collections.find(c => c.name === selected)?.count ?? 0}){:else}Select collection...{/if}
    </Select.Trigger>
    <Select.Content>
      {#each collections as col}
        <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
      {/each}
    </Select.Content>
  </Select.Root>
  <Button variant="outline" size="sm" onclick={handleImport}>Import</Button>
</div>
