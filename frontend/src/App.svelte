<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import * as Card from '$lib/components/ui/card';
  import * as Select from '$lib/components/ui/select';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import { Separator } from '$lib/components/ui/separator';
  import { Slider } from '$lib/components/ui/slider';
  import { SlidersHorizontalIcon, PaletteIcon, LockIcon, LockOpenIcon } from '@lucide/svelte';
  import PresetSelect from '$lib/components/PresetSelect.svelte';
  import ModelViewer from '$lib/components/ModelViewer.svelte';
  import CollectionPicker from '$lib/components/CollectionPicker.svelte';
  import CollectionSelect from '$lib/components/CollectionSelect.svelte';
  import CollectionManager from '$lib/components/CollectionManager.svelte';
  import { SharedCamera } from '$lib/components/SharedCamera.svelte';
  import { SelectInputFile, ProcessPipeline, SaveFile, LoadModelPreview, Version, LogMessage, GetCollectionColors } from '../wailsjs/go/main/App';
  import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';
  import type { pipeline } from '../wailsjs/go/models';

  // Log to Go stdout so it appears in the wails dev terminal as plain text.
  function log(msg: string) {
    LogMessage('info', msg);
  }

  // Form state with defaults matching CLI.
  let inputFile = $state('');
  let sizeMode: 'size' | 'scale' = $state('size');
  let sizeValue = $state('100');
  let scaleValue = $state('1.0');
  let nozzleDiameter = $state('0.4');
  let layerHeight = $state('0.20');
  // Color palette: each slot is either null (auto) or a locked color with hex + label + source collection.
  type ColorInfo = { hex: string; label: string; collection?: string };
  type ColorSlot = ColorInfo | null;
  let colorSlots = $state<ColorSlot[]>([null, null, null, null]);
  let pickerIndex = $state<number | null>(null);
  let colorSource: 'inventory' | 'auto' = $state('inventory');
  // For collection-based inventory source:
  let inventoryCollection = $state('Inventory');
  let inventoryCollectionColors = $state<{ hex: string; label: string }[]>([]);
  let brightness = $state(0);
  let contrast = $state(0);
  let saturation = $state(0);
  let dither = $state('dizzy');
  let colorSnap = $state(5);
  let noMerge = $state(false);
  let noSimplify = $state(false);
  let stats = $state(false);

  // Tab navigation.
  let activeTab: 'model' | 'collections' = $state('model');

  function addColorSlot() {
    if (colorSlots.length < 16) {
      colorSlots = [...colorSlots, null];
    }
  }

  function removeColorSlot(index: number) {
    if (colorSlots.length > 1) {
      colorSlots = colorSlots.filter((_, i) => i !== index);
      if (pickerIndex === index) pickerIndex = null;
      else if (pickerIndex !== null && pickerIndex > index) pickerIndex--;
    }
  }

  function openPicker(index: number) {
    pickerIndex = pickerIndex === index ? null : index;
  }

  function pickColor(hex: string, label: string, collection: string) {
    if (pickerIndex === null) return;
    colorSlots[pickerIndex] = { hex, label, collection };
    pickerIndex = null;
  }

  function closePicker() {
    pickerIndex = null;
  }

  function toggleLock(index: number) {
    if (colorSlots[index] !== null) {
      // Unlock: set to auto.
      colorSlots[index] = null;
    } else if (resolvedBySlot[index]) {
      // Lock to the resolved color.
      colorSlots[index] = resolvedBySlot[index];
    }
  }

  function colorTooltip(c: ColorInfo): string {
    const parts = [c.hex];
    if (c.label) parts.push(c.label);
    if (c.collection) parts.push(`from ${c.collection}`);
    return parts.join('\n');
  }

  // UI state.
  let running = $state(false);
  let statusMessage = $state('');
  let statusType: 'idle' | 'success' | 'error' = $state('idle');
  let version = $state('');
  let forceDialogOpen = $state(false);
  let forceExtentMM = $state(0);

  // Binary mesh URLs for 3D viewers.
  let inputMeshUrl: string | undefined = $state(undefined);
  let outputMeshUrl: string | undefined = $state(undefined);
  let inputError: string | undefined = $state(undefined);

  // Resolved auto colors from the backend (the non-locked portion of the palette).
  let resolvedAutoColors = $state<ColorInfo[]>([]);

  // Per-slot resolved color: maps auto colors back to their slot positions.
  let resolvedBySlot = $derived.by((): (ColorInfo | null)[] => {
    let autoIdx = 0;
    return colorSlots.map(slot =>
      slot !== null ? null : (resolvedAutoColors[autoIdx++] ?? null)
    );
  });

  // Shared camera state — single source of truth for both viewers.
  const sharedCamera = new SharedCamera();

  // Auto-processing state (plain variables, not reactive -- nothing in the template reads these).
  let processTimer: number | undefined;

  // Generation counter: tracks the latest pipeline request submitted.
  // Pipeline result events with gen < latestGen are stale and ignored.
  let latestGen = 0;

  // Separate generation counter for mesh events. Mesh events use their own
  // monotonic counter to prevent out-of-order delivery (e.g. LoadModelPreview
  // racing with the pipeline worker) from overwriting newer data.
  let meshGeneration = 0;


  Version().then(v => version = v);

  // Listen for binary mesh URLs from the backend.
  EventsOn('input-mesh', (event: { gen: number; url: string }) => {
    if (event.gen >= meshGeneration) {
      meshGeneration = event.gen;
      inputMeshUrl = event.url;
      inputError = undefined;
    }
  });
  EventsOn('output-mesh', (event: { gen: number; url: string }) => {
    if (event.gen >= meshGeneration) {
      meshGeneration = event.gen;
      outputMeshUrl = event.url;
    }
  });

  // Listen for pipeline result events from the backend worker.
  EventsOn('pipeline-done', (event: { gen: number; duration: number }) => {
    if (event.gen < latestGen) return;
    running = false;
    statusMessage = `Done! (${event.duration.toFixed(1)}s)`;
    statusType = 'success';
  });
  EventsOn('pipeline-error', (event: { gen: number; message: string }) => {
    if (event.gen < latestGen) return;
    running = false;
    statusMessage = `Error: ${event.message}`;
    statusType = 'error';
  });
  EventsOn('pipeline-needs-force', (event: { gen: number; extentMM: number }) => {
    if (event.gen < latestGen) return;
    running = false;
    forceExtentMM = event.extentMM;
    forceDialogOpen = true;
    statusMessage = '';
    statusType = 'idle';
  });
  EventsOn('palette-resolved', (event: { gen: number; colors: { hex: string; label: string }[] }) => {
    if (event.gen < latestGen) return;
    // The palette is [locked..., auto...]. Extract the auto portion.
    const numLocked = colorSlots.filter(s => s !== null).length;
    const collName = colorSource === 'inventory' ? inventoryCollection : '';
    resolvedAutoColors = event.colors.slice(numLocked).map(c => ({ ...c, collection: collName }));
  });

  function scheduleProcess(delay = 300) {
    clearTimeout(processTimer);
    if (!inputFile) return;
    if (delay > 0) {
      processTimer = window.setTimeout(() => runPipeline(), delay);
    } else {
      runPipeline();
    }
  }

  // Watch all form values and auto-trigger processing.
  let initialized = false;
  $effect(() => {
    // Read all form values to establish tracking.
    void [inputFile, sizeMode, sizeValue, scaleValue, nozzleDiameter,
          layerHeight, ...colorSlots, colorSource,
          inventoryCollectionColors,
          brightness, contrast, saturation,
          dither, colorSnap, noMerge, noSimplify, stats];
    if (!initialized) {
      initialized = true;
      return;
    }
    scheduleProcess(300);
  });

  async function browseInput() {
    const path = await SelectInputFile();
    if (path) {
      inputFile = path;
      loadInputPreview(path);
    }
  }

  // Load collection colors for inventory source.
  async function loadInventoryCollectionColors(name: string) {
    if (!name) {
      inventoryCollectionColors = [];
      return;
    }
    const colors = (await GetCollectionColors(name)) ?? [];
    inventoryCollectionColors = colors.map(c => ({ hex: c.hex, label: c.label }));
  }

  // Load initial inventory collection colors.
  loadInventoryCollectionColors(inventoryCollection);

  // Parse hex "#RRGGBB" to [r, g, b] array.
  function hexToRgb(hex: string): number[] {
    const h = hex.replace('#', '');
    return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
  }

  async function loadInputPreview(path: string) {
    try {
      inputError = undefined;
      await LoadModelPreview(path);
    } catch (err) {
      inputMeshUrl = undefined;
      inputError = String(err);
    }
  }

  function buildOpts(force: boolean): pipeline.Options {
    const invEntries = (colorSource === 'inventory') ? inventoryCollectionColors : [];
    const invColors = invEntries.map(c => hexToRgb(c.hex));
    const invLabels = invEntries.map(c => c.label);

    const opts: Partial<pipeline.Options> = {
      Input: inputFile,
      NumColors: colorSlots.length,
      LockedColors: colorSlots.filter((s): s is ColorInfo => s !== null).map(s => s.hex),
      AutoColors: colorSource === 'auto',
      Scale: sizeMode === 'scale' ? (parseFloat(scaleValue) || 1.0) : 1.0,
      NozzleDiameter: parseFloat(nozzleDiameter) || 0.4,
      LayerHeight: parseFloat(layerHeight) || 0.2,
      InventoryFile: '',
      InventoryColors: invColors,
      InventoryLabels: invLabels,
      Brightness: brightness,
      Contrast: contrast,
      Saturation: saturation,
      Dither: dither,
      NoMerge: noMerge,
      NoSimplify: noSimplify,
      Force: force,
      Stats: stats,
      ColorSnap: colorSnap,
    };

    if (sizeMode === 'size' && sizeValue) opts.Size = parseFloat(sizeValue);

    return opts as pipeline.Options;
  }

  async function runPipeline(force = false) {
    if (!inputFile) {
      statusMessage = 'Please select an input file.';
      statusType = 'error';
      return;
    }
    running = true;
    statusMessage = 'Processing...';
    statusType = 'idle';
    outputMeshUrl = undefined;
    resolvedAutoColors = [] as ColorInfo[];

    // ProcessPipeline enqueues the request and returns immediately.
    // The backend worker processes only the latest request and delivers
    // results via events (pipeline-done, pipeline-error, pipeline-needs-force).
    const gen = await ProcessPipeline(buildOpts(force));
    latestGen = gen;
  }

  let saving = $state(false);

  async function saveToFile() {
    saving = true;
    try {
      const path = await SaveFile();
      if (path) {
        statusMessage = `Saved to ${path}`;
        statusType = 'success';
      }
    } catch (err: any) {
      statusMessage = `Save error: ${err}`;
      statusType = 'error';
    } finally {
      saving = false;
    }
  }
</script>

<main class="h-screen flex">
  <!-- Icon rail -->
  <div class="w-12 min-w-12 flex flex-col items-center py-4 gap-2 border-r bg-muted/30">
    <button
      class="w-9 h-9 rounded-lg flex items-center justify-center transition-colors {activeTab === 'model' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
      title="Model Settings"
      onclick={() => activeTab = 'model'}
    >
      <SlidersHorizontalIcon size={18} />
    </button>
    <button
      class="w-9 h-9 rounded-lg flex items-center justify-center transition-colors {activeTab === 'collections' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
      title="Filament Collections"
      onclick={() => activeTab = 'collections'}
    >
      <PaletteIcon size={18} />
    </button>
  </div>

  <!-- Left panel: fixed width for model settings, full width for collections -->
  <div class="{activeTab === 'model' ? 'w-[480px] min-w-[400px]' : 'flex-1'} min-h-0 flex flex-col">
  {#if activeTab === 'model'}
    <div class="flex-1 flex flex-col p-6 overflow-y-auto">
    <h1 class="text-2xl font-bold mb-1"><a href="https://github.com/rtwfroody/ditherforge" onclick={(e) => { e.preventDefault(); BrowserOpenURL('https://github.com/rtwfroody/ditherforge'); }} class="hover:underline">DitherForge</a> {#if version}<span class="text-base font-normal text-muted-foreground">{version.replace(/^ditherforge\s*/i, '')}</span>{/if}</h1>
    <p class="text-sm text-muted-foreground mb-4">Convert textured 3D models to multi-material 3MF files</p>

    <Card.Root class="shrink-0">
      <Card.Content class="pt-6 space-y-4">
        <!-- Input file -->
        <div class="space-y-2">
          <Label for="input">Input file</Label>
          <div class="flex gap-2">
            <Input id="input" bind:value={inputFile} placeholder="Select a .glb or .3mf file" class="flex-1" />
            <Button variant="outline" onclick={browseInput}>Browse</Button>
          </div>
        </div>

        <Separator />

        <!-- Core settings -->
        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <div class="flex items-center gap-4">
              <label class="flex items-center gap-1.5 text-sm font-medium">
                <input type="radio" name="sizemode" value="size" checked={sizeMode === 'size'} onchange={() => { sizeMode = 'size'; }} />
                Size (mm)
              </label>
              <label class="flex items-center gap-1.5 text-sm font-medium">
                <input type="radio" name="sizemode" value="scale" checked={sizeMode === 'scale'} onchange={() => { sizeMode = 'scale'; }} />
                Scale
              </label>
            </div>
            {#if sizeMode === 'size'}
              <Input id="size" bind:value={sizeValue} type="number" step={1} />
            {:else}
              <Input id="scale" bind:value={scaleValue} type="number" step={0.1} />
            {/if}
          </div>
          <PresetSelect
            bind:value={nozzleDiameter}
            label="Nozzle diameter (mm)"
            id="nozzle"
            unit="mm"
            step={0.1}
            presets={[
              { value: '0.2', label: '0.2 mm' },
              { value: '0.4', label: '0.4 mm' },
              { value: '0.6', label: '0.6 mm' },
              { value: '0.8', label: '0.8 mm' },
            ]}
          />
          <PresetSelect
            bind:value={layerHeight}
            label="Layer height (mm)"
            id="layer"
            unit="mm"
            step={0.04}
            presets={[
              { value: '0.08', label: '0.08 mm' },
              { value: '0.12', label: '0.12 mm' },
              { value: '0.16', label: '0.16 mm' },
              { value: '0.20', label: '0.20 mm' },
              { value: '0.24', label: '0.24 mm' },
              { value: '0.28', label: '0.28 mm' },
            ]}
          />
        </div>

        <Separator />

        <!-- Color adjustments -->
        <div class="space-y-3">
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label>Brightness</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{brightness}</span>
            </div>
            <Slider type="single" min={-100} max={100} step={1} value={brightness} onValueChange={(v: number) => brightness = v} />
          </div>
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label>Contrast</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{contrast}</span>
            </div>
            <Slider type="single" min={-100} max={100} step={1} value={contrast} onValueChange={(v: number) => contrast = v} />
          </div>
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label>Saturation</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{saturation}</span>
            </div>
            <Slider type="single" min={-100} max={100} step={1} value={saturation} onValueChange={(v: number) => saturation = v} />
          </div>
        </div>

        <Separator />

        <!-- Color settings -->
        <div class="space-y-4">
          <!-- Color palette grid -->
          <div class="space-y-2">
            <Label>Palette</Label>
            <div class="grid grid-cols-4 gap-2">
              {#each colorSlots as slot, i}
                {@const resolved = resolvedBySlot[i]}
                <div class="group relative">
                  <button
                    type="button"
                    class="w-full h-12 rounded cursor-pointer flex items-center justify-center text-xs select-none {pickerIndex === i ? 'ring-2 ring-primary' : ''} {slot ? 'border' : resolved ? 'border border-dashed' : 'border'}"
                    style={slot ? `background: ${slot.hex};` : resolved ? `background: ${resolved.hex}; opacity: 0.7;` : 'background: var(--muted);'}
                    title={slot ? colorTooltip(slot) : resolved ? colorTooltip(resolved) : 'auto'}
                    onclick={() => openPicker(i)}
                  >
                    {#if slot}
                      <span class="px-1 rounded" style="background: rgba(0,0,0,0.4); color: white;">{slot.label || slot.hex}</span>
                    {:else if resolved}
                      <span class="px-1 rounded" style="background: rgba(0,0,0,0.4); color: white;">{resolved.label || resolved.hex}</span>
                    {:else}
                      <span class="text-muted-foreground">auto</span>
                    {/if}
                  </button>
                  <!-- Lock toggle -->
                  {#if slot || resolved}
                    <button
                      class="absolute top-0.5 left-0.5 flex items-center justify-center cursor-pointer rounded {slot ? 'w-4 h-4 bg-black/50' : 'w-4 h-4 opacity-0 group-hover:opacity-100 transition-opacity'}"
                      title={slot ? 'Unlock (set to auto)' : 'Lock this color'}
                      onmousedown={(e: MouseEvent) => { e.stopPropagation(); toggleLock(i); }}
                    >
                      {#if slot}
                        <LockIcon size={10} class="text-white" />
                      {:else}
                        <LockOpenIcon size={10} class="text-white drop-shadow-[0_1px_1px_rgba(0,0,0,0.8)]" />
                      {/if}
                    </button>
                  {/if}
                  {#if colorSlots.length > 1}
                    <button
                      class="absolute -top-1 -right-1 w-4 h-4 rounded-full bg-destructive text-destructive-foreground text-xs leading-none opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center"
                      onmousedown={(e: MouseEvent) => { e.stopPropagation(); removeColorSlot(i); }}
                    >&times;</button>
                  {/if}
                </div>
              {/each}
              {#if colorSlots.length < 16}
                <button
                  class="w-full h-12 rounded border-2 border-dashed border-muted-foreground/30 flex items-center justify-center text-muted-foreground hover:border-muted-foreground/60 hover:text-foreground transition-colors cursor-pointer"
                  onclick={addColorSlot}
                >+</button>
              {/if}
            </div>
            {#if pickerIndex !== null}
              <CollectionPicker
                onselect={pickColor}
                onclose={closePicker}
              />
            {/if}
          </div>

          <!-- Remaining color source -->
          <div class="space-y-2">
            <Label>Unlocked colors from</Label>
            <div class="flex gap-4">
              <label class="flex items-center gap-1.5 text-sm">
                <input type="radio" name="colorsource" value="inventory" checked={colorSource === 'inventory'} onchange={() => { colorSource = 'inventory'; }} />
                Collection
              </label>
              <label class="flex items-center gap-1.5 text-sm">
                <input type="radio" name="colorsource" value="auto" checked={colorSource === 'auto'} onchange={() => { colorSource = 'auto'; }} />
                Optimal
              </label>
            </div>
            {#if colorSource === 'inventory'}
              <CollectionSelect
                bind:selected={inventoryCollection}
                onchange={loadInventoryCollectionColors}
              />
            {/if}
          </div>

          <!-- Color snap -->
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label>Color snap (delta E)</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{colorSnap}</span>
            </div>
            <Slider type="single" min={0} max={50} step={1} value={colorSnap} onValueChange={(v: number) => colorSnap = v} />
          </div>
        </div>

        <Separator />

        <!-- Advanced (collapsed) -->
        <details>
          <summary class="text-sm font-medium cursor-pointer select-none">Advanced</summary>
          <div class="mt-3 space-y-4">
            <div class="grid grid-cols-2 gap-4">
              <div class="space-y-2">
                <Label for="dither">Dither mode</Label>
                <Select.Root type="single" bind:value={dither}>
                  <Select.Trigger class="w-full">
                    {dither || 'Select...'}
                  </Select.Trigger>
                  <Select.Content>
                    <Select.Item value="dizzy">dizzy</Select.Item>
                    <Select.Item value="none">none</Select.Item>
                  </Select.Content>
                </Select.Root>
              </div>
            </div>

            <div class="flex flex-wrap gap-x-6 gap-y-3">
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={noMerge} />
                No merge
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={noSimplify} />
                No simplify
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={stats} />
                Stats
              </label>
            </div>
          </div>
        </details>
      </Card.Content>
    </Card.Root>

    <!-- Action -->
    <div class="mt-4 flex items-center gap-4">
      <Button onclick={saveToFile} disabled={!outputMeshUrl || running || saving} size="lg">
        {saving ? 'Saving...' : 'Save'}
      </Button>

      {#if statusMessage}
        <p class="text-sm {statusType === 'success' ? 'text-green-500' : statusType === 'error' ? 'text-red-500' : 'text-muted-foreground'}">
          {statusMessage}
        </p>
      {/if}
    </div>

    </div>
  {:else}
    <CollectionManager />
  {/if}
  </div>

  <!-- Right column: 3D viewers (only shown on model tab) -->
  {#if activeTab === 'model'}
    <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
      <div class="flex-1 min-h-0">
        <ModelViewer meshUrl={inputMeshUrl} label="Input Model" viewerId="input" camera={sharedCamera} errorMessage={inputError} {brightness} {contrast} {saturation} />
      </div>
      <div class="flex-1 min-h-0">
        <ModelViewer meshUrl={outputMeshUrl} label="Output Model" viewerId="output" camera={sharedCamera} />
      </div>
    </div>
  {/if}
</main>

<AlertDialog.Root bind:open={forceDialogOpen}>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title>Model is very large</AlertDialog.Title>
      <AlertDialog.Description>
        The model extent is {Math.round(forceExtentMM)} mm, which exceeds the 300 mm safety limit. Continue anyway?
      </AlertDialog.Description>
    </AlertDialog.Header>
    <AlertDialog.Footer>
      <AlertDialog.Cancel>Cancel</AlertDialog.Cancel>
      <AlertDialog.Action onclick={() => { forceDialogOpen = false; runPipeline(true); }}>Continue</AlertDialog.Action>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>
