<script lang="ts">
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Button } from '$lib/components/ui/button';
  import { Slider } from '$lib/components/ui/slider';
  import * as Select from '$lib/components/ui/select';
  import { contrastColor } from '$lib/utils';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { XIcon, PlusIcon, PipetteIcon } from '@lucide/svelte';

  type WarpPin = {
    sourceHex: string;
    targetHex: string;
    targetLabel: string;
    sigma: number;
  };

  let {
    pins = $bindable([]),
    loadCollectionColors,
    pickingIndex = $bindable(-1),
    onStartPick,
  }: {
    pins: WarpPin[];
    loadCollectionColors: (name: string) => Promise<{ hex: string; label: string }[]>;
    pickingIndex?: number;
    onStartPick?: (index: number) => void;
  } = $props();

  // Track which pin index has the target picker open, and which collection it shows.
  let targetPickerIndex = $state<number | null>(null);
  let pickerCollection = $state('');

  collectionStore.ensureLoaded();

  // Colors for the target picker, loaded on demand.
  let pickerColors = $state<{ hex: string; label: string }[]>([]);

  async function openTargetPicker(index: number) {
    if (targetPickerIndex === index) {
      targetPickerIndex = null;
      return;
    }
    targetPickerIndex = index;
    // Default to the active collection if we haven't picked one yet.
    if (!pickerCollection && collectionStore.activeCollection) {
      pickerCollection = collectionStore.activeCollection;
    }
    if (pickerCollection) {
      await loadPickerColors(pickerCollection);
    }
  }

  async function loadPickerColors(name: string) {
    pickerColors = (await loadCollectionColors(name)) ?? [];
  }

  async function selectPickerCollection(name: string) {
    pickerCollection = name;
    await loadPickerColors(name);
  }

  function selectTarget(pinIndex: number, hex: string, label: string) {
    pins[pinIndex] = { ...pins[pinIndex], targetHex: hex, targetLabel: label };
    pins = pins; // trigger reactivity
    targetPickerIndex = null;
  }

  const MAX_PINS = 8;

  function addPin() {
    if (pins.length >= MAX_PINS) return;
    pins = [...pins, { sourceHex: '', targetHex: '', targetLabel: '', sigma: 5 }];
  }

  function removePin(index: number) {
    pins = pins.filter((_, i) => i !== index);
    if (targetPickerIndex === index) targetPickerIndex = null;
    else if (targetPickerIndex !== null && targetPickerIndex > index) targetPickerIndex--;
  }

  function updateSourceHex(index: number, value: string) {
    pins[index] = { ...pins[index], sourceHex: value };
    pins = pins;
  }

  function updateSigma(index: number, value: number) {
    pins[index] = { ...pins[index], sigma: value };
    pins = pins;
  }
</script>

<div class="space-y-2">
  <div class="flex items-center justify-between">
    <Label>Color Pins</Label>
    <Button variant="ghost" size="sm" onclick={addPin} class="h-6 px-2" disabled={pins.length >= MAX_PINS}>
      <PlusIcon class="h-3 w-3 mr-1" />Add
    </Button>
  </div>

  {#each pins as pin, i}
    <div class="border rounded-md p-2 space-y-2 bg-muted/30">
      <div class="flex items-center gap-2">
        <!-- Source color: swatch + eyedropper + hex input -->
        <div class="flex items-center gap-1 flex-1 min-w-0">
          <div
            class="w-6 h-6 rounded border shrink-0"
            style="background: {pin.sourceHex && /^#[0-9a-fA-F]{6}$/.test(pin.sourceHex) ? pin.sourceHex : '#888'};"
          ></div>
          <Button
            variant="ghost"
            size="sm"
            class="h-7 w-7 p-0 shrink-0 {pickingIndex === i ? 'ring-2 ring-primary bg-primary/10' : ''}"
            title="Pick color from input model"
            onclick={() => onStartPick?.(i)}
          >
            <PipetteIcon class="h-3.5 w-3.5" />
          </Button>
          <Input
            value={pin.sourceHex}
            oninput={(e: Event) => updateSourceHex(i, (e.target as HTMLInputElement).value)}
            placeholder="#RRGGBB"
            class="h-7 text-xs font-mono"
          />
        </div>

        <span class="text-xs text-muted-foreground shrink-0">→</span>

        <!-- Target color swatch button -->
        <button
          type="button"
          class="h-7 px-2 rounded border cursor-pointer flex items-center gap-1 text-xs shrink-0 {targetPickerIndex === i ? 'ring-2 ring-primary' : ''}"
          style="background: {pin.targetHex || '#888'}; color: {contrastColor(pin.targetHex || '#888')};"
          onclick={() => openTargetPicker(i)}
        >
          {pin.targetLabel || pin.targetHex || 'Pick...'}
        </button>

        <Button variant="ghost" size="sm" class="h-6 w-6 p-0 shrink-0" onclick={() => removePin(i)}>
          <XIcon class="h-3 w-3" />
        </Button>
      </div>

      <!-- Strength slider -->
      <div class="flex items-center gap-2">
        <span class="text-xs text-muted-foreground w-14 shrink-0">Reach</span>
        <Slider
          type="single"
          min={1}
          max={100}
          step={1}
          value={pin.sigma}
          onValueChange={(v: number) => updateSigma(i, v)}
          class="flex-1"
        />
        <span class="text-xs text-muted-foreground w-6 text-right">{pin.sigma}</span>
      </div>

      <!-- Target picker (inline, shown below this pin) -->
      {#if targetPickerIndex === i}
        <div class="border rounded bg-popover p-2 space-y-2">
          <Select.Root type="single" value={pickerCollection} onValueChange={(v: string) => selectPickerCollection(v)}>
            <Select.Trigger class="h-7 text-xs">
              {pickerCollection || 'Select collection...'}
            </Select.Trigger>
            <Select.Content>
              {#each collectionStore.collections as col}
                <Select.Item value={col.name}>{col.name} ({col.count})</Select.Item>
              {/each}
            </Select.Content>
          </Select.Root>
          <div class="grid grid-cols-4 gap-1 max-h-36 overflow-y-auto">
            {#each pickerColors as color}
              <button
                type="button"
                class="h-7 rounded border cursor-pointer flex items-center justify-center text-[10px] leading-tight select-none hover:ring-2 hover:ring-primary transition-shadow"
                style="background: {color.hex}; color: {contrastColor(color.hex)};"
                title={color.label || color.hex}
                onclick={() => selectTarget(i, color.hex, color.label)}
              >
                {color.label || color.hex}
              </button>
            {/each}
          </div>
        </div>
      {/if}
    </div>
  {/each}

  {#if pins.length === 0}
    <p class="text-xs text-muted-foreground">No color pins. Add one to map a specific color to a filament.</p>
  {/if}
</div>
