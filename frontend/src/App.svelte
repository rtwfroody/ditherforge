<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import * as Card from '$lib/components/ui/card';
  import * as Select from '$lib/components/ui/select';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Separator } from '$lib/components/ui/separator';
  import { Slider } from '$lib/components/ui/slider';
  import { LockIcon, LockOpenIcon, LoaderCircleIcon, SunIcon, MoonIcon } from '@lucide/svelte';
  import * as Menubar from '$lib/components/ui/menubar';
  import PresetSelect from '$lib/components/PresetSelect.svelte';
  import ModelViewer from '$lib/components/ModelViewer.svelte';
  import CollectionPicker from '$lib/components/CollectionPicker.svelte';
  import CollectionSelect from '$lib/components/CollectionSelect.svelte';
  import ColorPinEditor from '$lib/components/ColorPinEditor.svelte';
  import CollectionManager from '$lib/components/CollectionManager.svelte';
  import StickerPanel from '$lib/components/StickerPanel.svelte';
  import ObjectPicker from '$lib/components/ObjectPicker.svelte';
  import type { StickerUI } from '$lib/components/StickerPanel.svelte';
  import { SharedCamera } from '$lib/components/SharedCamera.svelte';
  import { contrastColor } from '$lib/utils';
  import { ProcessPipeline, Export3MF, SaveSettings, SaveSettingsDialog, OpenFileDialog, LoadSettingsFile, DefaultSettingsPath, Version, LogMessage, GetCollectionColors, ImportCollection, CreateCollection, DeleteCollection, OpenStickerImage, EnumerateObjects } from '../wailsjs/go/main/App';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';
  import type { pipeline, loader } from '../wailsjs/go/models';

  // Log to Go stdout so it appears in the wails dev terminal as plain text.
  function log(msg: string) {
    LogMessage('info', msg);
  }

  // Shorten a path for display. Always shows at least the full filename.
  function shortenPath(path: string, maxLen = 40): string {
    if (path.length <= maxLen) return path;
    const name = path.split(/[/\\]/).pop() ?? path;
    if (name.length >= maxLen - 3) return name;
    return '...' + path.slice(-(maxLen - 3));
  }

  // Dark mode toggle — persisted in localStorage, falls back to system preference.
  let darkMode = $state(
    localStorage.getItem('theme')
      ? localStorage.getItem('theme') === 'dark'
      : window.matchMedia('(prefers-color-scheme: dark)').matches
  );
  function toggleDarkMode() {
    darkMode = !darkMode;
    document.documentElement.classList.toggle('dark', darkMode);
    localStorage.setItem('theme', darkMode ? 'dark' : 'light');
  }

  // Form state with defaults matching CLI.
  let inputFile = $state('');
  let sizeMode: 'size' | 'scale' = $state('size');
  let sizeValue = $state('100');
  let scaleValue = $state('1.0');
  let nozzleDiameter = $state('0.4');
  let layerHeight = $state('0.20');
  // Base color for untextured faces: null = use model default, or {hex, label, collection}.
  let baseColor = $state<ColorInfo | null>(null);
  let baseColorPickerOpen = $state(false);
  // Color palette: each slot is either null (auto) or a locked color with hex + label + source collection.
  type ColorInfo = { hex: string; label: string; collection?: string };
  type ColorSlot = ColorInfo | null;
  let colorSlots = $state<ColorSlot[]>([null, null, null, null]);
  let pickerIndex = $state<number | null>(null);
  // For collection-based inventory source:
  let inventoryCollection = $state('Inventory');
  let inventoryCollectionColors = $state<{ hex: string; label: string }[]>([]);
  let brightness = $state(0);
  let contrast = $state(0);
  let saturation = $state(0);
  type WarpPinUI = { sourceHex: string; targetHex: string; targetLabel: string; sigma: number };
  let warpPins = $state<WarpPinUI[]>([]);
  let pickingPinIndex = $state(-1); // -1 = not picking
  let dither = $state('dizzy');
  let colorSnap = $state(5);
  let noMerge = $state(false);
  let noSimplify = $state(false);
  let uniformGrid = $state(false);
  let stats = $state(false);
  let stickers = $state<StickerUI[]>([]);
  let placingStickerIndex = $state(-1);
  let previewScale = $state(1); // set when input-mesh event includes it

  // Recent files (persisted in localStorage).
  const MAX_RECENT = 10;
  function loadRecentFiles(): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem('recentFiles') || '[]');
      return Array.isArray(raw) ? raw.filter((x: unknown) => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }
  let recentFiles = $state<string[]>(loadRecentFiles());

  function addRecentFile(path: string) {
    recentFiles = [path, ...recentFiles.filter(p => p !== path)].slice(0, MAX_RECENT);
    localStorage.setItem('recentFiles', JSON.stringify(recentFiles));
  }

  function removeRecentFile(path: string) {
    recentFiles = recentFiles.filter(p => p !== path);
    localStorage.setItem('recentFiles', JSON.stringify(recentFiles));
  }

  // Collection editor dialog state.
  let collectionDialogOpen = $state(false);
  let newCollectionDialogOpen = $state(false);
  let newCollectionName = $state('');
  let deleteCollectionDialogOpen = $state(false);

  // Settings file state.
  let settingsPath = $state('');  // current save path; empty = unsaved

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
  let inputError = $state('');

  // Resolved unlocked colors from the backend (the non-locked portion of the palette).
  let resolvedUnlockedColors = $state<ColorInfo[]>([]);

  // Pipeline progress stages for the output viewer.
  type StageInfo = {
    name: string;
    status: 'running' | 'done';
    hasProgress: boolean;
    current: number;
    total: number;
    startedAt: number;  // Date.now() when stage started
    elapsed: number;    // final elapsed seconds (set on done)
  };
  let pipelineStages = $state<StageInfo[]>([]);
  let stageTick = $state(0);  // incremented to force timer re-render
  let stageTimerHandle = 0;

  // Tick running stage timers so the display updates.
  function startStageTimer() {
    if (stageTimerHandle) return;
    stageTimerHandle = window.setInterval(() => {
      const hasRunning = pipelineStages.some(s => s.status === 'running');
      if (hasRunning) {
        stageTick++;
      } else {
        window.clearInterval(stageTimerHandle);
        stageTimerHandle = 0;
      }
    }, 100);
  }

  onDestroy(() => {
    if (stageTimerHandle) window.clearInterval(stageTimerHandle);
  });

  // Per-slot resolved color: maps unlocked colors back to their slot positions.
  let resolvedBySlot = $derived.by((): (ColorInfo | null)[] => {
    let idx = 0;
    return colorSlots.map(slot =>
      slot !== null ? null : (resolvedUnlockedColors[idx++] ?? null)
    );
  });

  // Shared camera state — single source of truth for both viewers.
  const sharedCamera = new SharedCamera();

  // Auto-processing state (plain variables, not reactive -- nothing in the template reads these).
  let processTimer: number | undefined;

  // Generation counter: tracks the latest pipeline request submitted.
  // Pipeline result events with gen < latestGen are stale and ignored.
  let latestGen = 0;

  // All mesh and pipeline events use latestGen for staleness checks.

  Version().then(v => version = v);

  // Listen for binary mesh URLs from the backend.
  EventsOn('input-mesh', (event: { gen: number; url: string; previewScale?: number }) => {
    if (event.gen < latestGen) return;
    inputMeshUrl = event.url;
    if (event.previewScale) previewScale = event.previewScale;
  });
  EventsOn('output-mesh', (event: { gen: number; url: string }) => {
    if (event.gen < latestGen) return;
    outputMeshUrl = event.url;
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
    inputError = event.message;
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
    const collName = inventoryCollection;
    resolvedUnlockedColors = event.colors.slice(numLocked).map(c => ({ ...c, collection: collName }));
  });
  EventsOn('pipeline-stage', (event: { gen: number; stage: string; status: string; hasProgress: boolean; total: number }) => {
    if (event.gen < latestGen) return;
    const now = Date.now();
    if (event.status === 'running') {
      const existing = pipelineStages.find(s => s.name === event.stage);
      if (existing) {
        existing.status = 'running';
        existing.hasProgress = event.hasProgress;
        existing.total = event.total;
        existing.current = 0;
        existing.startedAt = now;
        existing.elapsed = 0;
        pipelineStages = pipelineStages;
      } else {
        pipelineStages = [...pipelineStages, {
          name: event.stage,
          status: 'running',
          hasProgress: event.hasProgress,
          current: 0,
          total: event.total,
          startedAt: now,
          elapsed: 0,
        }];
      }
      startStageTimer();
    } else if (event.status === 'done') {
      const existing = pipelineStages.find(s => s.name === event.stage);
      if (existing) {
        existing.status = 'done';
        existing.elapsed = (now - existing.startedAt) / 1000;
        pipelineStages = pipelineStages;
      } else {
        pipelineStages = [...pipelineStages, {
          name: event.stage,
          status: 'done',
          hasProgress: false,
          current: 0,
          total: 0,
          startedAt: now,
          elapsed: 0,
        }];
      }
    }
  });
  EventsOn('pipeline-progress', (event: { gen: number; stage: string; current: number }) => {
    if (event.gen < latestGen) return;
    const existing = pipelineStages.find(s => s.name === event.stage);
    if (existing) {
      existing.current = event.current;
      pipelineStages = pipelineStages;
    }
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
          layerHeight, baseColor, ...colorSlots,
          inventoryCollectionColors,
          brightness, contrast, saturation,
          JSON.stringify(warpPins),
          JSON.stringify(stickers),
          dither, colorSnap, noMerge, noSimplify, uniformGrid, stats];
    if (!initialized) {
      initialized = true;
      return;
    }
    scheduleProcess(300);
  });

  function handleColorPick(hex: string) {
    if (pickingPinIndex >= 0 && pickingPinIndex < warpPins.length) {
      warpPins[pickingPinIndex] = { ...warpPins[pickingPinIndex], sourceHex: hex };
      warpPins = warpPins;
      pickingPinIndex = -1;
    }
  }

  async function addSticker() {
    const path = await OpenStickerImage();
    if (!path) return;
    const fileName = path.split(/[/\\]/).pop() ?? path;
    stickers = [...stickers, {
      imagePath: path,
      fileName,
      center: null,
      normal: null,
      up: null,
      scale: 20,
      rotation: 0,
      maxAngle: 0,
    }];
    // Automatically enter placement mode for the new sticker.
    placingStickerIndex = stickers.length - 1;
  }

  function removeSticker(index: number) {
    stickers = stickers.filter((_, i) => i !== index);
    if (placingStickerIndex === index) placingStickerIndex = -1;
    else if (placingStickerIndex > index) placingStickerIndex--;
  }

  function handleStickerPlace(point: [number, number, number], normal: [number, number, number], cameraUp: [number, number, number]) {
    if (placingStickerIndex < 0 || placingStickerIndex >= stickers.length) return;
    // Convert from preview-scaled coordinates to pipeline coordinates.
    const unscaled: [number, number, number] = [
      point[0] / previewScale,
      point[1] / previewScale,
      point[2] / previewScale,
    ];
    stickers[placingStickerIndex] = {
      ...stickers[placingStickerIndex],
      center: unscaled,
      normal,
      up: cameraUp,
    };
    stickers = stickers;
    placingStickerIndex = -1;
  }

  let reloadSeq = $state(0);
  let objectIndex = $state(-1); // -1 = all objects, >=0 = specific object
  let objectPickerOpen = $state(false);
  let objectPickerItems = $state<loader.ObjectInfo[]>([]);
  let pendingInputPath = $state('');

  async function openInputModel(path: string) {
    const ext = path.split('.').pop()?.toLowerCase();
    if (ext === '3mf' || ext === 'glb') {
      try {
        const objects = await EnumerateObjects(path);
        if (objects && objects.length > 1) {
          pendingInputPath = path;
          objectPickerItems = objects;
          objectPickerOpen = true;
          return; // wait for user selection in dialog
        }
      } catch (e) {
        // Fall through to load normally
      }
    }
    objectIndex = -1;
    proceedWithInput(path);
  }

  function onObjectSelected(idx: number) {
    objectIndex = idx;
    proceedWithInput(pendingInputPath);
    pendingInputPath = '';
  }

  function proceedWithInput(path: string) {
    inputMeshUrl = undefined;
    outputMeshUrl = undefined;
    inputFile = path;
    reloadSeq++;
    DefaultSettingsPath(path).then(p => settingsPath = p).catch(() => {});
    addRecentFile(path);
    // Force a pipeline run even if the path didn't change (file on disk may
    // have changed). The $effect won't fire when inputFile is unchanged, so
    // schedule explicitly.
    scheduleProcess(0);
  }

  // Reset the camera pose whenever the input model changes — no matter
  // how (File > Open, loading a settings JSON, etc.).
  let prevInputFile = '';
  $effect(() => {
    if (inputFile !== prevInputFile) {
      prevInputFile = inputFile;
      sharedCamera.initialized = false;
    }
  });

  function serializeSettings() {
    return {
      inputFile,
      objectIndex,
      sizeMode,
      sizeValue: String(sizeValue),
      scaleValue: String(scaleValue),
      nozzleDiameter: String(nozzleDiameter),
      layerHeight: String(layerHeight),
      baseColor: baseColor ? { hex: baseColor.hex, label: baseColor.label, collection: baseColor.collection } : null,
      colorSlots: colorSlots.map(s => s ? { hex: s.hex, label: s.label, collection: s.collection } : null),
      inventoryCollection,
      brightness,
      contrast,
      saturation,
      warpPins: warpPins.map(p => ({ sourceHex: p.sourceHex, targetHex: p.targetHex, targetLabel: p.targetLabel, sigma: p.sigma })),
      stickers: stickers.filter(s => s.center !== null).map(s => ({
        imagePath: s.imagePath,
        center: s.center,
        normal: s.normal,
        up: s.up,
        scale: s.scale,
        rotation: s.rotation,
        maxAngle: s.maxAngle,
      })),
      dither,
      colorSnap,
      noMerge,
      noSimplify,
      uniformGrid,
      stats,
    };
  }

  function applySettings(s: any) {
    if (s.inputFile !== undefined) inputFile = s.inputFile;
    objectIndex = s.objectIndex ?? -1;
    if (s.sizeMode !== undefined) sizeMode = s.sizeMode;
    if (s.sizeValue !== undefined) sizeValue = s.sizeValue;
    if (s.scaleValue !== undefined) scaleValue = s.scaleValue;
    if (s.nozzleDiameter !== undefined) nozzleDiameter = s.nozzleDiameter;
    if (s.layerHeight !== undefined) layerHeight = s.layerHeight;
    if (s.baseColor !== undefined) baseColor = s.baseColor ? { hex: s.baseColor.hex, label: s.baseColor.label || '', collection: s.baseColor.collection || '' } : null;
    if (s.colorSlots !== undefined) {
      colorSlots = s.colorSlots.map((c: any) => c ? { hex: c.hex, label: c.label || '', collection: c.collection || '' } : null);
    }
    if (s.inventoryCollection !== undefined) {
      inventoryCollection = s.inventoryCollection;
      loadInventoryCollectionColors(inventoryCollection);
    }
    if (s.brightness !== undefined) brightness = s.brightness;
    if (s.contrast !== undefined) contrast = s.contrast;
    if (s.saturation !== undefined) saturation = s.saturation;
    if (s.warpPins !== undefined) {
      warpPins = s.warpPins.map((p: any) => ({ sourceHex: p.sourceHex, targetHex: p.targetHex, targetLabel: p.targetLabel || '', sigma: p.sigma }));
    }
    if (s.stickers !== undefined) {
      stickers = s.stickers.map((st: any) => ({
        imagePath: st.imagePath,
        fileName: (st.imagePath || '').split(/[/\\]/).pop() || st.imagePath,
        center: st.center,
        normal: st.normal,
        up: st.up,
        scale: st.scale,
        rotation: st.rotation,
        maxAngle: st.maxAngle ?? 0,
      }));
    }
    if (s.dither !== undefined) dither = s.dither;
    if (s.colorSnap !== undefined) colorSnap = s.colorSnap;
    if (s.noMerge !== undefined) noMerge = s.noMerge;
    if (s.noSimplify !== undefined) noSimplify = s.noSimplify;
    if (s.uniformGrid !== undefined) uniformGrid = s.uniformGrid;
    if (s.stats !== undefined) stats = s.stats;
  }

  async function handleSave() {
    if (!settingsPath) {
      return handleSaveAs();
    }
    try {
      await SaveSettings(settingsPath, serializeSettings() as any);
      addRecentFile(settingsPath);
      statusMessage = `Saved to ${settingsPath}`;
      statusType = 'success';
    } catch (err: any) {
      statusMessage = `Save error: ${err}`;
      statusType = 'error';
    }
  }

  async function handleSaveAs() {
    try {
      const path = await SaveSettingsDialog(serializeSettings() as any);
      if (path) {
        settingsPath = path;
        addRecentFile(path);
        statusMessage = `Saved to ${path}`;
        statusType = 'success';
      }
    } catch (err: any) {
      statusMessage = `Save error: ${err}`;
      statusType = 'error';
    }
  }

  async function openFile(path: string) {
    try {
      const ext = path.split('.').pop()?.toLowerCase();
      if (ext === 'json') {
        const result = await LoadSettingsFile(path);
        if (result && result.path) {
          settingsPath = result.path;
          applySettings(result.settings);
          addRecentFile(path);
          statusMessage = `Loaded from ${result.path}`;
          statusType = 'success';
        }
      } else {
        await openInputModel(path);
      }
    } catch (err: any) {
      removeRecentFile(path);
      statusMessage = `Open error: ${err}`;
      statusType = 'error';
    }
  }

  async function handleOpen() {
    const path = await OpenFileDialog();
    if (!path) return;
    await openFile(path);
  }

  function clearRecentFiles() {
    recentFiles = [];
    localStorage.removeItem('recentFiles');
  }

  // Filaments menu handlers.
  collectionStore.ensureLoaded();


  function openCollection(name: string) {
    collectionStore.select(name);
    collectionDialogOpen = true;
  }

  async function handleImportCollection() {
    try {
      const name = await ImportCollection();
      if (name) {
        await collectionStore.refresh();
        openCollection(name);
      }
    } catch (err) {
      console.error('Failed to import collection:', err);
    }
  }

  async function handleDeleteCollection() {
    const name = collectionStore.activeCollection;
    if (!name) return;
    try {
      await DeleteCollection(name);
      collectionDialogOpen = false;
      deleteCollectionDialogOpen = false;
      collectionStore.activeCollection = '';
      collectionStore.colors = [];
      await collectionStore.refresh();
    } catch (err) {
      console.error('Failed to delete collection:', err);
    }
  }

  async function handleCreateCollection() {
    if (!newCollectionName.trim()) return;
    try {
      await CreateCollection(newCollectionName.trim());
      newCollectionDialogOpen = false;
      await collectionStore.refresh();
      openCollection(newCollectionName.trim());
    } catch (err) {
      console.error('Failed to create collection:', err);
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

  function buildOpts(force: boolean): pipeline.Options {
    const invEntries = inventoryCollectionColors;
    const invColors = invEntries.map(c => hexToRgb(c.hex));
    const invLabels = invEntries.map(c => c.label);

    const opts: Partial<pipeline.Options> = {
      Input: inputFile,
      NumColors: colorSlots.length,
      LockedColors: colorSlots.filter((s): s is ColorInfo => s !== null).map(s => s.hex),
      Scale: sizeMode === 'scale' ? (parseFloat(scaleValue) || 1.0) : 1.0,
      BaseColor: baseColor?.hex ?? '',
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
      UniformGrid: uniformGrid,
      Force: force,
      ReloadSeq: reloadSeq,
      ObjectIndex: objectIndex,
      Stats: stats,
      ColorSnap: colorSnap,
      WarpPins: warpPins
        .filter(p => /^#[0-9a-fA-F]{6}$/.test(p.sourceHex) && /^#[0-9a-fA-F]{6}$/.test(p.targetHex))
        .map(p => ({ sourceHex: p.sourceHex, targetHex: p.targetHex, sigma: p.sigma })),
      Stickers: stickers
        .filter(s => s.center !== null)
        .map(s => ({
          ImagePath: s.imagePath,
          Center: s.center!,
          Normal: s.normal!,
          Up: s.up!,
          Scale: s.scale,
          Rotation: s.rotation,
          MaxAngle: s.maxAngle,
        })),
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
    inputError = '';
    statusMessage = 'Processing...';
    statusType = 'idle';
    outputMeshUrl = undefined;
    resolvedUnlockedColors = [] as ColorInfo[];
    pipelineStages = [];
    if (stageTimerHandle) {
      window.clearInterval(stageTimerHandle);
      stageTimerHandle = 0;
    }

    // ProcessPipeline enqueues the request and returns immediately.
    // The backend worker processes only the latest request and delivers
    // results via events (pipeline-done, pipeline-error, pipeline-needs-force).
    const gen = await ProcessPipeline(buildOpts(force));
    latestGen = gen;
  }

  let saving = $state(false);
  let saveError = $state('');

  async function exportTo3MF() {
    saving = true;
    saveError = '';
    try {
      const path = await Export3MF();
      if (path) {
        statusMessage = `Exported to ${path}`;
        statusType = 'success';
      }
    } catch (err: any) {
      saveError = String(err);
    } finally {
      saving = false;
    }
  }
</script>

<main class="h-screen flex flex-col">
  <!-- Menu bar -->
  <Menubar.Root class="rounded-none border-b border-t-0 border-x-0">
    <Menubar.Menu>
      <Menubar.Trigger>File</Menubar.Trigger>
      <Menubar.Content>
        <Menubar.Item onSelect={handleOpen}>Open...</Menubar.Item>
        <Menubar.Sub>
          <Menubar.SubTrigger disabled={recentFiles.length === 0}>Open Recent</Menubar.SubTrigger>
          <Menubar.SubContent align="start">
            {#each recentFiles as path}
              <Menubar.Item onSelect={() => openFile(path)}>
                {path.split(/[/\\]/).pop()}
                <span class="text-muted-foreground ml-auto pl-4 text-xs truncate max-w-48" title={path}>{shortenPath(path)}</span>
              </Menubar.Item>
            {/each}
            <Menubar.Separator />
            <Menubar.Item onSelect={clearRecentFiles}>Clear Recent</Menubar.Item>
          </Menubar.SubContent>
        </Menubar.Sub>
        <Menubar.Item onSelect={handleSave} disabled={!settingsPath}>Save JSON</Menubar.Item>
        <Menubar.Item onSelect={handleSaveAs}>Save JSON As...</Menubar.Item>
        <Menubar.Separator />
        <Menubar.Item onSelect={exportTo3MF} disabled={!outputMeshUrl || running || saving}>Export 3MF...</Menubar.Item>
      </Menubar.Content>
    </Menubar.Menu>
    <Menubar.Menu>
      <Menubar.Trigger>Filaments</Menubar.Trigger>
      <Menubar.Content>
        {#each collectionStore.collections as col}
          <Menubar.Item onSelect={() => openCollection(col.name)}>
            {col.name} <span class="text-muted-foreground ml-auto pl-4">{col.count}</span>
          </Menubar.Item>
        {/each}
        {#if collectionStore.collections.length > 0}
          <Menubar.Separator />
        {/if}
        <Menubar.Item onSelect={() => { newCollectionName = ''; newCollectionDialogOpen = true; }}>New...</Menubar.Item>
        <Menubar.Item onSelect={handleImportCollection}>Import...</Menubar.Item>
      </Menubar.Content>
    </Menubar.Menu>
    <div class="ml-auto flex items-center gap-2 pr-2">
      {#if settingsPath || inputFile}
        <span class="text-xs text-muted-foreground truncate max-w-64" title={settingsPath || inputFile}>{shortenPath(settingsPath || inputFile)}</span>
      {/if}
      <button
        class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
        title={darkMode ? 'Switch to light mode' : 'Switch to dark mode'}
        onclick={toggleDarkMode}
      >
        {#if darkMode}<SunIcon size={16} />{:else}<MoonIcon size={16} />{/if}
      </button>
    </div>
  </Menubar.Root>

  <div class="flex-1 flex min-h-0">
  <!-- Left panel -->
  <div class="w-[480px] min-w-[400px] min-h-0 flex flex-col">
    <div class="flex-1 flex flex-col p-6 overflow-y-auto">
    <h1 class="text-2xl font-bold mb-1"><a href="https://github.com/rtwfroody/ditherforge" onclick={(e) => { e.preventDefault(); BrowserOpenURL('https://github.com/rtwfroody/ditherforge'); }} class="hover:underline">DitherForge</a> {#if version}<span class="text-base font-normal text-muted-foreground">{version.replace(/^ditherforge\s*/i, '')}</span>{/if}</h1>
    <p class="text-sm text-muted-foreground mb-4">Convert textured 3D models to multi-material 3MF files</p>

    <Card.Root class="shrink-0">
      <Card.Content class="pt-6 space-y-4">
        <!-- Core settings -->
        <div class="grid grid-cols-2 gap-x-4 gap-y-2 items-end">
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
          <span class="text-sm font-medium">Base color</span>
          {#if sizeMode === 'size'}
            <Input id="size" bind:value={sizeValue} type="number" step={1} />
          {:else}
            <Input id="scale" bind:value={scaleValue} type="number" step={0.1} />
          {/if}
          {#if baseColor}
            <div class="flex items-center gap-2">
              <button
                class="h-9 flex-1 rounded border cursor-pointer flex items-center justify-center text-xs px-2 gap-1.5 hover:ring-2 hover:ring-primary transition-shadow"
                style="background: {baseColor.hex}; color: {contrastColor(baseColor.hex)};"
                title={colorTooltip(baseColor)}
                onclick={() => { baseColorPickerOpen = !baseColorPickerOpen; }}
              >
                {baseColor.label || baseColor.hex}
              </button>
              <Button variant="ghost" size="sm" onclick={() => { baseColor = null; baseColorPickerOpen = false; }}>Clear</Button>
            </div>
          {:else}
            <Button variant="outline" class="w-full" size="sm" onclick={() => { baseColorPickerOpen = !baseColorPickerOpen; }}>
              Default
            </Button>
          {/if}
          {#if baseColorPickerOpen}
            <div class="col-span-2">
              <CollectionPicker
                onselect={(hex, label, collection) => { baseColor = { hex, label, collection }; baseColorPickerOpen = false; }}
                onclose={() => { baseColorPickerOpen = false; }}
              />
            </div>
          {/if}
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

        <!-- Stickers -->
        <StickerPanel
          bind:stickers={stickers}
          bind:placingIndex={placingStickerIndex}
          onAdd={addSticker}
          onRemove={removeSticker}
        />

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

        <!-- Color pins -->
        <ColorPinEditor
          bind:pins={warpPins}
          loadCollectionColors={GetCollectionColors}
          bind:pickingIndex={pickingPinIndex}
          onStartPick={(i: number) => pickingPinIndex = pickingPinIndex === i ? -1 : i}
        />

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
                    class="w-full rounded cursor-pointer flex flex-col select-none overflow-hidden {pickerIndex === i ? 'ring-2 ring-primary' : ''} {slot ? 'border' : resolved ? 'border border-dashed' : 'border'}"
                    title={slot ? colorTooltip(slot) : resolved ? colorTooltip(resolved) : 'auto'}
                    onclick={() => openPicker(i)}
                  >
                    {#if slot || resolved}
                      {@const info = (slot ?? resolved)!}
                      <div class="w-full h-5 shadow-[inset_0_0_0_1px_rgba(0,0,0,0.1)]" style="background: {info.hex};"></div>
                      <div class="w-full px-1 py-0.5 text-[11px] leading-tight text-center text-foreground break-words border-t border-border">{info.label || info.hex}</div>
                    {:else}
                      <div class="w-full h-5 bg-muted"></div>
                      <div class="w-full px-1 py-0.5 text-[11px] leading-tight text-center text-muted-foreground border-t border-border">auto</div>
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
                  class="w-full rounded border-2 border-dashed border-muted-foreground/30 flex items-center justify-center text-muted-foreground hover:border-muted-foreground/60 hover:text-foreground transition-colors cursor-pointer py-2"
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
            <CollectionSelect
              bind:selected={inventoryCollection}
              onchange={loadInventoryCollectionColors}
            />
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
                <Checkbox bind:checked={uniformGrid} />
                Uniform grid
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

    {#if statusMessage}
    <div class="mt-4">
      <p class="text-sm {statusType === 'success' ? 'text-green-500' : statusType === 'error' ? 'text-red-500' : 'text-muted-foreground'}">
        {statusMessage}
      </p>
    </div>
    {/if}

    </div>
  </div>

  <!-- Right column: 3D viewers -->
  <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
    <div class="flex-1 min-h-0">
      <ModelViewer meshUrl={inputMeshUrl} label={inputFile ? `Input Model: ${shortenPath(inputFile)}` : 'Input Model'} viewerId="input" camera={sharedCamera} {brightness} {contrast} {saturation} pickMode={pickingPinIndex >= 0} stickerPlaceMode={placingStickerIndex >= 0} onColorPick={handleColorPick} onStickerPlace={handleStickerPlace} warpPins={pickingPinIndex >= 0 ? [] : warpPins} loading={inputFile ? inputFile.split('/').pop() ?? '' : ''} errorMessage={inputError} />
    </div>
    <div class="flex-1 min-h-0">
      <ModelViewer meshUrl={outputMeshUrl} label="Output Model" viewerId="output" camera={sharedCamera} stages={pipelineStages} {stageTick} />
    </div>
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

<Dialog.Root open={saving || !!saveError} onOpenChange={(open) => { if (!open) saveError = ''; }}>
  <Dialog.Content showCloseButton={!!saveError} onInteractOutside={(e) => { if (saving) e.preventDefault(); }} onEscapeKeydown={(e) => { if (saving) e.preventDefault(); }}>
    <Dialog.Header>
      <Dialog.Title>{saveError ? 'Export failed' : 'Exporting...'}</Dialog.Title>
      <Dialog.Description>
        {#if saveError}
          {saveError}
        {:else}
          <span class="flex items-center gap-2">
            <LoaderCircleIcon class="w-4 h-4 animate-spin" />
            Writing 3MF file...
          </span>
        {/if}
      </Dialog.Description>
    </Dialog.Header>
    {#if saveError}
      <Dialog.Footer>
        <Dialog.Close>
          {#snippet child({ props })}
            <Button variant="outline" {...props}>Close</Button>
          {/snippet}
        </Dialog.Close>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<Dialog.Root bind:open={collectionDialogOpen}>
  <Dialog.Content class="sm:max-w-4xl max-h-[80vh] overflow-y-auto overflow-x-hidden">
    <Dialog.Header>
      <Dialog.Title>{collectionStore.activeCollection}</Dialog.Title>
    </Dialog.Header>
    <CollectionManager />
    {#if collectionStore.isEditable}
      <Dialog.Footer>
        <Button variant="destructive" size="sm" class="text-foreground" onclick={() => { deleteCollectionDialogOpen = true; }}>Delete Collection</Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>

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

<AlertDialog.Root bind:open={deleteCollectionDialogOpen}>
  <AlertDialog.Content>
    <AlertDialog.Header>
      <AlertDialog.Title>Delete collection</AlertDialog.Title>
      <AlertDialog.Description>
        Are you sure you want to delete "{collectionStore.activeCollection}"? This cannot be undone.
      </AlertDialog.Description>
    </AlertDialog.Header>
    <AlertDialog.Footer>
      <AlertDialog.Cancel>Cancel</AlertDialog.Cancel>
      <AlertDialog.Action onclick={handleDeleteCollection}>Delete</AlertDialog.Action>
    </AlertDialog.Footer>
  </AlertDialog.Content>
</AlertDialog.Root>

<ObjectPicker
  objects={objectPickerItems}
  bind:open={objectPickerOpen}
  onSelect={onObjectSelected}
/>
