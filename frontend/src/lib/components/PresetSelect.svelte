<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import * as Select from '$lib/components/ui/select';

  let {
    value = $bindable(''),
    label,
    id,
    presets,
    unit,
    step,
  }: {
    value: string;
    label: string;
    id: string;
    presets: { value: string; label: string }[];
    unit: string;
    step: number;
  } = $props();

  let custom = $state(false);
</script>

<div class="space-y-2">
  <Label for={id}>{label}</Label>
  {#if custom}
    <div class="flex gap-2">
      <Input {id} bind:value type="number" {step} class="flex-1" />
      <Button variant="outline" size="sm" onclick={() => { custom = false; }}>Preset</Button>
    </div>
  {:else}
    <Select.Root type="single" {value} onValueChange={(v) => { if (v === 'other') { custom = true; } else { value = v; } }}>
      <Select.Trigger class="w-full">
        {value ? value + ' ' + unit : 'Select...'}
      </Select.Trigger>
      <Select.Content>
        {#each presets as preset}
          <Select.Item value={preset.value}>{preset.label}</Select.Item>
        {/each}
        <Select.Item value="other">Other...</Select.Item>
      </Select.Content>
    </Select.Root>
  {/if}
</div>
