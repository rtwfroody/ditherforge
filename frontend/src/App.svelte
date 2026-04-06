<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import * as Card from '$lib/components/ui/card';
  import * as Select from '$lib/components/ui/select';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import { Separator } from '$lib/components/ui/separator';
  import PresetSelect from '$lib/components/PresetSelect.svelte';
  import ModelViewer from '$lib/components/ModelViewer.svelte';
  import { SharedCamera } from '$lib/components/SharedCamera.svelte';
  import { SelectInputFile, ProcessPipeline, SaveFile, LoadModelPreview, Version, LogMessage } from '../wailsjs/go/main/App';
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
  let numColors = $state('4');
  let lockedColors = $state<string[]>([]);
  let colorSource: 'defaults' | 'inventory' | 'auto' = $state('defaults');
  let inventoryFile = $state('');
  let brightness = $state(0);
  let contrast = $state(0);
  let saturation = $state(0);
  let dither = $state('dizzy');
  let colorSnap = $state('5');
  let noMerge = $state(false);
  let noSimplify = $state(false);
  let stats = $state(false);

  // Pending locked color input.
  let newColorInput = $state('');

  function addLockedColor() {
    const c = newColorInput.trim();
    if (c && lockedColors.length < parseInt(numColors)) {
      lockedColors = [...lockedColors, c];
      newColorInput = '';
    }
  }

  function removeLockedColor(index: number) {
    lockedColors = lockedColors.filter((_, i) => i !== index);
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
          layerHeight, numColors, lockedColors, colorSource, inventoryFile,
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

  async function loadInputPreview(path: string) {
    try {
      await LoadModelPreview(path);
    } catch (err) {
      console.error('Failed to load preview:', err);
    }
  }

  function buildOpts(force: boolean): pipeline.Options {
    const opts: Partial<pipeline.Options> = {
      Input: inputFile,
      NumColors: parseInt(numColors) || 4,
      LockedColors: lockedColors.length > 0 ? lockedColors : [],
      AutoColors: colorSource === 'auto',
      Scale: sizeMode === 'scale' ? (parseFloat(scaleValue) || 1.0) : 1.0,
      NozzleDiameter: parseFloat(nozzleDiameter) || 0.4,
      LayerHeight: parseFloat(layerHeight) || 0.2,
      InventoryFile: colorSource === 'inventory' ? inventoryFile : '',
      Brightness: brightness,
      Contrast: contrast,
      Saturation: saturation,
      Dither: dither,
      NoMerge: noMerge,
      NoSimplify: noSimplify,
      Force: force,
      Stats: stats,
      ColorSnap: parseFloat(colorSnap) || 5,
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
  <!-- Left column: options form -->
  <div class="w-[480px] min-w-[400px] min-h-0 flex flex-col p-6 overflow-y-auto">
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
              <Label for="brightness">Brightness</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{brightness}</span>
            </div>
            <input id="brightness" type="range" min="-100" max="100" step="1" bind:value={brightness} class="w-full" />
          </div>
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label for="contrast">Contrast</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{contrast}</span>
            </div>
            <input id="contrast" type="range" min="-100" max="100" step="1" bind:value={contrast} class="w-full" />
          </div>
          <div class="space-y-1">
            <div class="flex items-center justify-between">
              <Label for="saturation">Saturation</Label>
              <span class="text-xs text-muted-foreground w-8 text-right">{saturation}</span>
            </div>
            <input id="saturation" type="range" min="-100" max="100" step="1" bind:value={saturation} class="w-full" />
          </div>
        </div>

        <Separator />

        <!-- Color settings -->
        <div class="space-y-4">
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label for="numcolors">Number of colors</Label>
              <Input id="numcolors" bind:value={numColors} type="number" min="1" max="16" step="1" />
            </div>
            <div class="space-y-2">
              <Label for="colorsnap">Color snap (delta E)</Label>
              <Input id="colorsnap" bind:value={colorSnap} type="number" step="1" />
            </div>
          </div>

          <!-- Locked colors -->
          <div class="space-y-2">
            <Label>Locked colors</Label>
            {#if lockedColors.length > 0}
              <div class="flex flex-wrap gap-2">
                {#each lockedColors as color, i}
                  <span class="inline-flex items-center gap-1 px-2 py-1 rounded bg-muted text-sm">
                    {color}
                    <button class="text-muted-foreground hover:text-foreground" onclick={() => removeLockedColor(i)}>&times;</button>
                  </span>
                {/each}
              </div>
            {/if}
            {#if lockedColors.length < parseInt(numColors)}
              <div class="flex gap-2">
                <Input bind:value={newColorInput} placeholder="CSS name or hex (e.g. black, #FF0000)" class="flex-1"
                  onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter') addLockedColor(); }} />
                <Button variant="outline" size="sm" onclick={addLockedColor}>+ Lock</Button>
              </div>
            {/if}
          </div>

          <!-- Remaining color source -->
          <div class="space-y-2">
            <Label>Remaining colors from</Label>
            <div class="flex gap-4">
              <label class="flex items-center gap-1.5 text-sm">
                <input type="radio" name="colorsource" value="defaults" checked={colorSource === 'defaults'} onchange={() => { colorSource = 'defaults'; }} />
                Defaults
              </label>
              <label class="flex items-center gap-1.5 text-sm">
                <input type="radio" name="colorsource" value="inventory" checked={colorSource === 'inventory'} onchange={() => { colorSource = 'inventory'; }} />
                Inventory
              </label>
              <label class="flex items-center gap-1.5 text-sm">
                <input type="radio" name="colorsource" value="auto" checked={colorSource === 'auto'} onchange={() => { colorSource = 'auto'; }} />
                Optimal
              </label>
            </div>
            {#if colorSource === 'inventory'}
              <Input bind:value={inventoryFile} placeholder="Inventory file path" />
            {/if}
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

  <!-- Right column: 3D viewers -->
  <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
    <div class="flex-1 min-h-0">
      <ModelViewer meshUrl={inputMeshUrl} label="Input Model" viewerId="input" camera={sharedCamera} {brightness} {contrast} {saturation} />
    </div>
    <div class="flex-1 min-h-0">
      <ModelViewer meshUrl={outputMeshUrl} label="Output Model" viewerId="output" camera={sharedCamera} />
    </div>
  </div>
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
