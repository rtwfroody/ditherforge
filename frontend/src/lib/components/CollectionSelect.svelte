<script lang="ts">
  import * as Select from '$lib/components/ui/select';
  import { collectionStore } from '$lib/stores/collections.svelte';

  let {
    selected = $bindable(''),
    onchange,
  }: {
    selected: string;
    onchange: (name: string) => void;
  } = $props();

  function handleChange(v: string) {
    selected = v;
    onchange(selected);
  }

  // Ensure collections are loaded; auto-select first if none selected.
  collectionStore.ensureLoaded();
  $effect(() => {
    if (!selected && collectionStore.collections.length > 0) {
      selected = collectionStore.collections[0].name;
      onchange(selected);
    }
  });
</script>

<div class="flex gap-2">
  <Select.Root type="single" value={selected} onValueChange={handleChange}>
    <Select.Trigger class="flex-1">
      {#if selected}{selected} ({collectionStore.collections.find(c => c.name === selected)?.count ?? 0}){:else}Select collection...{/if}
    </Select.Trigger>
    <Select.Content>
      {#each collectionStore.collections as col}
        <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
      {/each}
    </Select.Content>
  </Select.Root>
</div>
