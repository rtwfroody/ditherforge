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
  import ModelViewer, { type CameraAngles } from '$lib/components/ModelViewer.svelte';
  import { SelectInputFile, SelectOutputFile, PreparePipeline, RenderPipeline, LoadModelPreview, Version } from '../wailsjs/go/main/App';
  import type { pipeline } from '../wailsjs/go/models';

  // Form state with defaults matching CLI.
  let inputFile = $state('');
  let outputFile = $state('output.3mf');
  let sizeMode: 'size' | 'scale' = $state('size');
  let sizeValue = $state('100');
  let scaleValue = $state('1.0');
  let nozzleDiameter = $state('0.4');
  let layerHeight = $state('0.20');
  let palette = $state('');
  let autoPalette = $state('');
  let dither = $state('dizzy');
  let colorSnap = $state('5');
  let inventoryFile = $state('');
  let inventory = $state('');
  let noMerge = $state(false);
  let noSimplify = $state(false);
  let noCache = $state(false);
  let stats = $state(false);

  // UI state.
  let running = $state(false);
  let statusMessage = $state('');
  let statusType: 'idle' | 'success' | 'error' = $state('idle');
  let version = $state('');
  let forceDialogOpen = $state(false);
  let forceExtentMM = $state(0);

  // Track geometry options to detect when re-prepare is needed.
  let lastPrepareKey = $state('');

  // Mesh data for 3D viewers.
  let inputMesh: pipeline.MeshData | undefined = $state(undefined);
  let outputMesh: pipeline.MeshData | undefined = $state(undefined);

  // Shared camera angles to sync both viewers.
  let sharedAngles: CameraAngles | undefined = $state(undefined);

  function onCameraChange(angles: CameraAngles) {
    sharedAngles = angles;
  }

  Version().then(v => version = v);


  async function browseInput() {
    const path = await SelectInputFile();
    if (path) {
      inputFile = path;
      loadInputPreview(path);
    }
  }

  async function loadInputPreview(path: string) {
    try {
      inputMesh = await LoadModelPreview(path);
    } catch (err) {
      console.error('Failed to load preview:', err);
      inputMesh = undefined;
    }
  }

  async function browseOutput() {
    const path = await SelectOutputFile();
    if (path) outputFile = path;
  }

  function buildOpts(force: boolean): pipeline.Options {
    const opts: Partial<pipeline.Options> = {
      Input: inputFile,
      Output: outputFile,
      Scale: sizeMode === 'scale' ? (parseFloat(scaleValue) || 1.0) : 1.0,
      NozzleDiameter: parseFloat(nozzleDiameter) || 0.4,
      LayerHeight: parseFloat(layerHeight) || 0.2,
      Dither: dither,
      NoMerge: noMerge,
      NoSimplify: noSimplify,
      NoCache: noCache,
      Force: force,
      Stats: stats,
      ColorSnap: parseFloat(colorSnap) || 5,
      Palette: palette,
    };

    if (sizeMode === 'size' && sizeValue) opts.Size = parseFloat(sizeValue);
    if (autoPalette) opts.AutoPalette = parseInt(autoPalette);
    if (inventoryFile) opts.InventoryFile = inventoryFile;
    if (inventory) opts.Inventory = parseInt(inventory);

    return opts as pipeline.Options;
  }

  // Key that changes when geometry options change (requiring re-prepare).
  function prepareKey(): string {
    return JSON.stringify({
      inputFile, sizeMode, sizeValue, scaleValue,
      nozzleDiameter, layerHeight, noSimplify,
    });
  }

  function needsPrepare(): boolean {
    return prepareKey() !== lastPrepareKey;
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
    outputMesh = undefined;

    try {
      if (needsPrepare()) {
        statusMessage = 'Preparing...';
        const prepResult = await PreparePipeline(buildOpts(force));

        if (prepResult.NeedsForce) {
          forceExtentMM = prepResult.ModelExtentMM;
          forceDialogOpen = true;
          statusMessage = '';
          statusType = 'idle';
          return;
        }

        if (prepResult.InputMesh) {
          inputMesh = prepResult.InputMesh;
        }
        lastPrepareKey = prepareKey();
      }

      statusMessage = 'Rendering...';
      const result = await RenderPipeline(buildOpts(force));
      const secs = (result.Duration / 1e9).toFixed(1);
      statusMessage = `Done! Wrote ${result.OutputPath} (${result.FaceCount} faces, ${secs}s)`;
      statusType = 'success';
      if (result.OutputMesh) {
        outputMesh = result.OutputMesh;
      }
    } catch (err: any) {
      statusMessage = `Error: ${err}`;
      statusType = 'error';
    } finally {
      running = false;
    }
  }
</script>

<main class="h-screen flex">
  <!-- Left column: options form -->
  <div class="w-[480px] min-w-[400px] min-h-0 flex flex-col p-6 overflow-y-auto">
    <h1 class="text-2xl font-bold mb-1">DitherForge</h1>
    <p class="text-sm text-muted-foreground mb-4">Convert textured 3D models to multi-material 3MF files</p>

    <Card.Root class="shrink-0">
      <Card.Content class="pt-6 space-y-4">
        <!-- Input / Output -->
        <div class="space-y-2">
          <Label for="input">Input file</Label>
          <div class="flex gap-2">
            <Input id="input" bind:value={inputFile} placeholder="Select a .glb or .3mf file" class="flex-1" />
            <Button variant="outline" onclick={browseInput}>Browse</Button>
          </div>
        </div>

        <div class="space-y-2">
          <Label for="output">Output file</Label>
          <div class="flex gap-2">
            <Input id="output" bind:value={outputFile} placeholder="output.3mf" class="flex-1" />
            <Button variant="outline" onclick={browseOutput}>Browse</Button>
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

        <!-- Color settings -->
        <div class="space-y-4">
          <div class="space-y-2">
            <Label for="palette">Palette (comma-separated colors)</Label>
            <Input id="palette" bind:value={palette} placeholder="Default: best 4 of cyan,magenta,yellow,black,white,red,green,blue" />
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label for="autopalette">Auto palette (N colors)</Label>
              <Input id="autopalette" bind:value={autoPalette} type="number" placeholder="Off" />
            </div>
            <div class="space-y-2">
              <Label for="colorsnap">Color snap (delta E)</Label>
              <Input id="colorsnap" bind:value={colorSnap} type="number" step="1" />
            </div>
          </div>
        </div>

        <!-- Inventory -->
        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <Label for="invfile">Inventory file</Label>
            <Input id="invfile" bind:value={inventoryFile} placeholder="None" />
          </div>
          <div class="space-y-2">
            <Label for="invcount">Inventory pick N</Label>
            <Input id="invcount" bind:value={inventory} type="number" placeholder="Off" />
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
                <Checkbox bind:checked={noCache} />
                No cache
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
      <Button onclick={() => runPipeline()} disabled={running} size="lg">
        {running ? 'Processing...' : 'Go'}
      </Button>

      {#if statusMessage}
        <p class="text-sm {statusType === 'success' ? 'text-green-500' : statusType === 'error' ? 'text-red-500' : 'text-muted-foreground'}">
          {statusMessage}
        </p>
      {/if}
    </div>

    {#if version}
      <p class="mt-6 text-xs text-muted-foreground">{version}</p>
    {/if}
  </div>

  <!-- Right column: 3D viewers -->
  <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
    <div class="flex-1 min-h-0">
      <ModelViewer meshData={inputMesh} label="Input Model" viewerId="input" cameraAngles={sharedAngles} {onCameraChange} />
    </div>
    <div class="flex-1 min-h-0">
      <ModelViewer meshData={outputMesh} label="Output Model" viewerId="output" cameraAngles={sharedAngles} {onCameraChange} />
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
