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
  import { Slider } from '$lib/components/ui/slider';
  import * as Tooltip from '$lib/components/ui/tooltip';
  import HelpTip from '$lib/components/HelpTip.svelte';
  import SettingsSection from '$lib/components/SettingsSection.svelte';
  import SplitControls from '$lib/components/SplitControls.svelte';
  import { LockIcon, LockOpenIcon, LoaderCircleIcon, SunIcon, MoonIcon } from '@lucide/svelte';
  import * as Menubar from '$lib/components/ui/menubar';
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
  import type { CutPlanePreview } from '$lib/types';
  import { ProcessPipeline, Export3MF, SaveSettings, SaveSettingsDialog, OpenFileDialog, LoadSettingsFile, DefaultSettingsPath, Version, LogMessage, GetCollectionColors, ImportCollection, CreateCollection, DeleteCollection, OpenStickerImage, ReadStickerThumbnail, EnumerateObjects, ListPrinters, Quit } from '../wailsjs/go/main/App';
  import type { main } from '../wailsjs/go/models';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';
  import type { pipeline, loader } from '../wailsjs/go/models';

  // Log to Go stdout so it appears in the wails dev terminal as plain text.
  function log(msg: string) {
    LogMessage('info', msg);
  }

  // Shorten a path for display. Always shows at least the full filename.
  function shortenPath(path: string, maxLen = 80): string {
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
  // Printer registry — populated on mount via ListPrinters(). Defines the
  // allowed nozzle diameters and layer heights for the selected printer.
  let printers = $state<main.PrinterOption[]>([]);
  let printerId = $state('snapmaker_u1');
  let nozzleDiameter = $state('0.4');
  let layerHeight = $state('0.20');

  const currentPrinter = $derived<main.PrinterOption | undefined>(
    printers.find(p => p.id === printerId)
  );
  const currentNozzle = $derived<main.NozzleOption | undefined>(
    currentPrinter?.nozzles.find(n => n.diameter === nozzleDiameter)
  );

  function fmtLayerHeight(lh: number): string {
    return lh.toFixed(2);
  }

  // When the printer changes, snap nozzle + layer height to something valid
  // for the new printer. Preserve the user's current choice if possible.
  // If the registry has loaded and the selected printer isn't in it, fall
  // back to the default (first printer in registry).
  function reconcilePrinterSelection() {
    if (printers.length === 0) return; // registry not loaded yet
    let printer = printers.find(p => p.id === printerId);
    if (!printer) {
      printer = printers[0];
      printerId = printer.id;
    }
    if (!printer.nozzles.find(n => n.diameter === nozzleDiameter)) {
      const pick = printer.nozzles.find(n => n.diameter === '0.4')
        ?? printer.nozzles[0];
      nozzleDiameter = pick?.diameter ?? '';
    }
    const noz = printer.nozzles.find(n => n.diameter === nozzleDiameter);
    if (!noz) return;
    const current = parseFloat(layerHeight);
    const available = noz.layerHeights;
    if (!available.length) return;
    if (!available.find(v => Math.abs(v - current) < 1e-6)) {
      // Snap to closest available layer height, preferring 0.20 if equally far.
      let best = available[0];
      let bestD = Math.abs(best - current);
      for (const v of available) {
        const d = Math.abs(v - current);
        if (d < bestD || (d === bestD && Math.abs(v - 0.20) < Math.abs(best - 0.20))) {
          best = v;
          bestD = d;
        }
      }
      layerHeight = fmtLayerHeight(best);
    }
  }
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
  // Committed shadows: brightness/contrast/saturation/colorSnap update
  // continuously during slider drag for live preview, but the pipeline-
  // triggering $effect and the Process payload read the *committed*
  // versions, which only update on slider release. This avoids spawning
  // a backend pipeline run every 300ms while the user is still dragging.
  let committedBrightness = $state(0);
  let committedContrast = $state(0);
  let committedSaturation = $state(0);
  type WarpPinUI = { sourceHex: string; targetHex: string; targetLabel: string; sigma: number };
  let warpPins = $state<WarpPinUI[]>([]);
  let pickingPinIndex = $state(-1); // -1 = not picking
  let dither = $state('dizzy');
  let colorSnap = $state(5);
  let committedColorSnap = $state(5);
  let noMerge = $state(false);
  let noSimplify = $state(false);
  let stats = $state(false);
  let alphaWrap = $state(false);
  let alphaWrapAlpha = $state('');   // mm; '' = auto (5 × nozzle diameter)
  let alphaWrapOffset = $state('');  // mm; '' = auto (alpha / 30)
  // Split (cut model into two halves with peg/pocket connectors).
  // See docs/SPLIT.md. Defaults match the design doc's "what most
  // users want" baseline.
  let splitEnabled = $state(false);
  let splitAxis = $state(2); // 0=X, 1=Y, 2=Z
  let splitOffset = $state(0);
  let splitConnectorStyle = $state('pegs');
  let splitConnectorCount = $state(0); // 0 = auto
  let splitConnectorDiamMM = $state(5);
  let splitConnectorDepthMM = $state(6);
  let splitClearanceMM = $state(0.15);
  let splitGapMM = $state(5);
  // The loaded model's bbox in original-mesh coords (mm, post-scale,
  // post-normalizeZ). Populated from the input-mesh event; null until
  // the first event arrives so the Split UI can distinguish "no model
  // loaded yet" from "model with bbox at the origin."
  let modelBBoxMin = $state<[number, number, number] | null>(null);
  let modelBBoxMax = $state<[number, number, number] | null>(null);
  // Min/max for the Split offset slider — derived from the bbox along
  // the chosen axis. Updates automatically when the user toggles axes
  // or loads a new model. Falls back to a 0..100 placeholder while
  // bbox is unknown (the slider is rarely visible in that state since
  // Split.Enabled requires AlphaWrap, which requires a loaded model).
  const splitOffsetMin = $derived(modelBBoxMin?.[splitAxis] ?? 0);
  const splitOffsetMax = $derived(modelBBoxMax?.[splitAxis] ?? 100);

  // When the user changes the cut axis, recentre the offset on the
  // new axis's bbox midpoint. Without this, an offset that was sane
  // for axis Z would commonly fall outside the X or Y range.
  $effect(() => {
    const axis = splitAxis;
    if (!modelBBoxMin || !modelBBoxMax) return;
    const lo = modelBBoxMin[axis];
    const hi = modelBBoxMax[axis];
    if (splitOffset < lo || splitOffset > hi) {
      splitOffset = (lo + hi) / 2;
    }
  });

  // Cut-plane preview overlay for the input viewer. Mirrors the
  // backend's pipeline.computeSplitPreviewFromVertices in
  // internal/pipeline/splitpreview.go — keep the two in sync. The
  // (U, V) basis is right-handed with U × V = Normal, and the quad
  // is centred on the model's bbox so it sits symmetrically over the
  // mesh. Computed client-side from the bbox so it tracks the slider
  // without RPC churn.
  //
  // Assumes (U, V) are axis-aligned (one of {±X, ±Y, ±Z}). That lets
  // us compute min/max of the projected silhouette from the two
  // bbox-corner endpoints alone instead of every vertex — equivalent
  // because the projection of a bbox onto an axis-aligned vector is
  // determined entirely by its corners. If splitpreview.go ever
  // generalizes to arbitrary plane normals, this mirror must too.
  const cutPlanePreview = $derived.by((): CutPlanePreview | null => {
    if (!splitEnabled || !modelBBoxMin || !modelBBoxMax) return null;
    const axis = splitAxis;
    const normal: [number, number, number] = [0, 0, 0];
    normal[axis] = 1;
    let u: [number, number, number];
    let v: [number, number, number];
    switch (axis) {
      case 0: u = [0, 1, 0]; v = [0, 0, 1]; break;
      case 1: u = [0, 0, 1]; v = [1, 0, 0]; break;
      default: u = [1, 0, 0]; v = [0, 1, 0]; break;
    }
    const proj = (p: [number, number, number], a: [number, number, number]) =>
      p[0] * a[0] + p[1] * a[1] + p[2] * a[2];
    const minU = Math.min(proj(modelBBoxMin, u), proj(modelBBoxMax, u));
    const maxU = Math.max(proj(modelBBoxMin, u), proj(modelBBoxMax, u));
    const minV = Math.min(proj(modelBBoxMin, v), proj(modelBBoxMax, v));
    const maxV = Math.max(proj(modelBBoxMin, v), proj(modelBBoxMax, v));
    const originU = (minU + maxU) / 2;
    const originV = (minV + maxV) / 2;
    const origin: [number, number, number] = [0, 0, 0];
    origin[axis] = splitOffset;
    for (let i = 0; i < 3; i++) origin[i] += originU * u[i] + originV * v[i];
    // The bbox is in original-mesh mm but the input viewer renders the
    // mesh at previewScale (vertices multiplied by previewScale in
    // scalePreviewMesh). Scale origin and half-extents to match the
    // rendered frame; (u, v, normal) directions are scale-invariant.
    const ps = calibratedPreviewScale ?? 1;
    return {
      origin: [origin[0] * ps, origin[1] * ps, origin[2] * ps],
      normal,
      u,
      v,
      halfExtentU: ((maxU - minU) / 2) * ps,
      halfExtentV: ((maxV - minV) / 2) * ps,
    };
  });

  // Cascade: turning AlphaWrap off while Split is on auto-disables
  // Split (the cut needs a watertight input). The reverse cascade
  // (turning Split on auto-enables AlphaWrap) lives in
  // SplitControls.svelte's onAlphaWrapForced callback.
  $effect(() => {
    if (!alphaWrap && splitEnabled) {
      splitEnabled = false;
    }
  });
  let stickers = $state<StickerUI[]>([]);
  let placingStickerIndex = $state(-1);
  const placingSticker = $derived(placingStickerIndex >= 0 ? stickers[placingStickerIndex] ?? null : null);

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
  let statusType: 'idle' | 'success' | 'error' | 'warning' = $state('idle');
  let version = $state('');
  let forceDialogOpen = $state(false);
  let forceExtentMM = $state(0);

  // Binary mesh URLs for 3D viewers.
  let inputMeshUrl: string | undefined = $state(undefined);
  // Optional alpha-wrap sticker overlay. Rendered just outside the input
  // mesh in the input viewer; carries decals when alpha-wrap is on.
  let inputOverlayMeshUrl: string | undefined = $state(undefined);
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
  EventsOn('input-mesh', (event: { gen: number; url: string; previewScale?: number; extentMM?: number; bboxMin?: [number, number, number]; bboxMax?: [number, number, number] }) => {
    if (event.gen < latestGen) return;
    inputMeshUrl = event.url;
    // The overlay is set/cleared deterministically by the
    // 'input-overlay-mesh' event the pipeline fires after this one, so
    // we don't need to clear it here.
    if (event.extentMM !== undefined && (nativeExtentMM === null || !approxEqual(nativeExtentMM, event.extentMM))) {
      nativeExtentMM = event.extentMM;
    }
    if (event.previewScale !== undefined) {
      applyPreviewScale(event.previewScale);
    }
    // Update the model bbox for the Split offset slider. The bbox
    // is in original-mesh coords (mm, post-scale, post-normalizeZ).
    if (event.bboxMin && event.bboxMax) {
      const newMin = event.bboxMin;
      const newMax = event.bboxMax;
      modelBBoxMin = newMin;
      modelBBoxMax = newMax;
      // If Split was enabled with the previous model's bbox in mind
      // (offset clamped to a range that's now invalid), recentre the
      // offset on the new model's bbox along the chosen axis.
      const lo = newMin[splitAxis];
      const hi = newMax[splitAxis];
      if (splitOffset < lo || splitOffset > hi) {
        splitOffset = (lo + hi) / 2;
      }
    }
  });
  EventsOn('input-overlay-mesh', (event: { gen: number; url: string }) => {
    if (event.gen < latestGen) return;
    // Empty url means the pipeline explicitly told us there's no overlay
    // (e.g. alpha-wrap turned off). Clear the previous overlay if any.
    inputOverlayMeshUrl = event.url || undefined;
  });
  EventsOn('output-mesh', (event: { gen: number; url: string }) => {
    if (event.gen < latestGen) return;
    outputMeshUrl = event.url;
  });

  // Listen for pipeline result events from the backend worker.
  EventsOn('pipeline-done', (event: { gen: number; duration: number }) => {
    if (event.gen < latestGen) return;
    running = false;
    // Preserve any warning emitted during the run; don't overwrite it
    // with "Done!".
    if (statusType !== 'warning') {
      statusMessage = `Done! (${event.duration.toFixed(1)}s)`;
      statusType = 'success';
    }
  });
  EventsOn('pipeline-error', (event: { gen: number; message: string }) => {
    if (event.gen < latestGen) return;
    running = false;
    inputError = event.message;
    statusMessage = `Error: ${event.message}`;
    statusType = 'error';
  });
  EventsOn('pipeline-warning', (event: { gen: number; message: string }) => {
    if (event.gen < latestGen) return;
    if (statusType !== 'error') {
      statusMessage = event.message;
      statusType = 'warning';
    }
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
    void [inputFile, sizeMode, sizeValue, scaleValue, printerId, nozzleDiameter,
          layerHeight, baseColor, ...colorSlots,
          inventoryCollectionColors,
          committedBrightness, committedContrast, committedSaturation,
          JSON.stringify(warpPins),
          JSON.stringify(stickers),
          dither, committedColorSnap, noMerge, noSimplify, stats,
          alphaWrap, alphaWrapAlpha, alphaWrapOffset,
          splitEnabled, splitAxis, splitOffset,
          splitConnectorStyle, splitConnectorCount,
          splitConnectorDiamMM, splitConnectorDepthMM,
          splitClearanceMM, splitGapMM,
          reloadSeq];
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
    let thumbnail = '';
    try { thumbnail = await ReadStickerThumbnail(path); } catch { /* ignore */ }
    stickers = [...stickers, {
      imagePath: path,
      fileName,
      thumbnail,
      center: null,
      normal: null,
      up: null,
      scale: 20,
      rotation: 0,
      maxAngle: 0,
      mode: 'projection',
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
    // Convert from preview-scaled coordinates to pipeline coordinates. Use
    // calibratedPreviewScale so the new sticker lands in the same frame as
    // existing stickers, even if knobs were just changed and the backend
    // hasn't yet re-run the pipeline.
    const ps = calibratedPreviewScale ?? 1;
    const unscaled: [number, number, number] = [
      point[0] / ps,
      point[1] / ps,
      point[2] / ps,
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

  // Sticker coords live in pipeline-scaled units. The pipeline's previewScale
  // (= unitScale/totalScale, emitted by the backend on each input-mesh) is
  // the single scale sticker coords are calibrated against. When it changes,
  // we rescale stickers by the inverse ratio. null means "sticker coords
  // carry no known calibration — adopt whatever we next observe/predict,
  // without rescaling" (set on fresh load and applySettings, since saved
  // coords are consistent with the accompanying knobs).
  let calibratedPreviewScale: number | null = $state(null);
  // Native max extent of the loaded model in mm (scale=1.0, size=unset).
  // Reported by the backend; enables predicting previewScale synchronously
  // so sticker rescales happen on knob change rather than after pipeline re-run.
  let nativeExtentMM: number | null = $state(null);

  // predictPreviewScale algebraically mirrors backend's `unitScale/totalScale`.
  // Values match within float32 precision; see SCALE_EPS for the numerical slack.
  //   size mode:  totalScale = Size/nativeExtentFile
  //               previewScale = unitScale*nativeExtentFile/Size = extentMM/Size
  //   scale mode: totalScale = unitScale*Scale → previewScale = 1/Scale
  // Returns null when inputs are incomplete (in size mode, when extentMM is
  // not yet known — typical just after load).
  function predictPreviewScale(mode: 'size' | 'scale', sizeStr: string, scaleStr: string): number | null {
    if (mode === 'size') {
      if (nativeExtentMM === null || nativeExtentMM <= 0) return null;
      const s = parseFloat(sizeStr);
      if (!isFinite(s) || s <= 0) return null;
      return nativeExtentMM / s;
    }
    const k = parseFloat(scaleStr);
    if (!isFinite(k) || k <= 0) return null;
    return 1 / k;
  }

  // Tolerance for treating two previewScales (or extents) as equal. Backend
  // reports float32 values; frontend predictions go through a different
  // arithmetic path, so exact equality is unreliable. Without a tolerance,
  // float-roundoff micro-rescales would retrigger the pipeline in a loop.
  const SCALE_EPS = 1e-5;
  function approxEqual(a: number, b: number) {
    const d = Math.abs(a - b);
    const m = Math.max(Math.abs(a), Math.abs(b));
    return d <= SCALE_EPS * (m > 0 ? m : 1);
  }

  // Move sticker calibration to newPS. If uncalibrated (null) adopt. If
  // already at newPS within tolerance, do nothing (don't overwrite, to avoid
  // waking up reactive readers for a no-op). Otherwise rescale stickers by
  // the totalScale ratio (= oldPS/newPS) and update the calibration.
  function applyPreviewScale(newPS: number) {
    const cur = calibratedPreviewScale;
    if (cur === null) {
      calibratedPreviewScale = newPS;
      return;
    }
    if (approxEqual(cur, newPS)) return;
    const ratio = cur / newPS;
    if (stickers.some(s => s.center !== null)) {
      stickers = stickers.map(s => s.center ? {
        ...s,
        center: [s.center[0] * ratio, s.center[1] * ratio, s.center[2] * ratio] as [number, number, number],
        scale: s.scale * ratio,
      } : s);
    }
    calibratedPreviewScale = newPS;
  }

  // Synchronous rescale on any knob change: if we can predict the new
  // previewScale, apply it immediately so stickers track the knobs without
  // waiting for the backend pipeline. Otherwise the input-mesh handler will
  // catch up with the authoritative value.
  //
  // Termination: applyPreviewScale writes calibratedPreviewScale, which is
  // read inside this effect (transitively, via applyPreviewScale → cur read).
  // That re-triggers the effect once; on re-entry predictPreviewScale returns
  // the same p, applyPreviewScale's approxEqual guard fires, no write occurs,
  // and the effect stops.
  $effect(() => {
    const p = predictPreviewScale(sizeMode, sizeValue, scaleValue);
    if (p !== null) applyPreviewScale(p);
  });

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
    inputOverlayMeshUrl = undefined;
    outputMeshUrl = undefined;
    // Bbox is per-model; clear so the Split UI doesn't briefly show
    // the prior model's range while the new mesh is loading.
    modelBBoxMin = null;
    modelBBoxMax = null;
    inputFile = path;
    // Stickers are tied to the previous model's geometry; clear them.
    // Warp pins reference colors sampled from the previous model; clear too.
    stickers = [];
    placingStickerIndex = -1;
    calibratedPreviewScale = null;
    nativeExtentMM = null;
    warpPins = [];
    pickingPinIndex = -1;
    reloadSeq++;
    // Clear settingsPath synchronously so a save before DefaultSettingsPath
    // resolves (or if it fails) can't write to the previous model's file.
    settingsPath = '';
    DefaultSettingsPath(path).then(p => settingsPath = p).catch(() => {});
    addRecentFile(path);
    // The $effect tracks reloadSeq, so bumping it above ensures the pipeline
    // runs even when the path is unchanged (file on disk may have changed).
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
      printer: printerId,
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
        mode: s.mode,
      })),
      dither,
      colorSnap,
      noMerge,
      noSimplify,
      stats,
      alphaWrap,
      alphaWrapAlpha: String(alphaWrapAlpha),
      alphaWrapOffset: String(alphaWrapOffset),
      splitEnabled,
      splitAxis,
      splitOffset,
      splitConnectorStyle,
      splitConnectorCount,
      splitConnectorDiamMM,
      splitConnectorDepthMM,
      splitClearanceMM,
      splitGapMM,
    };
  }

  function applySettings(s: any) {
    // Saved sticker coords match the saved size/scale settings. Clear
    // calibration so the next prediction/event is adopted without rescaling.
    // Also clear the cached extent: if this settings file points at a
    // different input model, the old extent would produce a bogus prediction
    // before the backend replies with the true extent.
    calibratedPreviewScale = null;
    nativeExtentMM = null;
    if (s.inputFile !== undefined) inputFile = s.inputFile;
    objectIndex = s.objectIndex ?? -1;
    if (s.sizeMode !== undefined) sizeMode = s.sizeMode;
    if (s.sizeValue !== undefined) sizeValue = s.sizeValue;
    if (s.scaleValue !== undefined) scaleValue = s.scaleValue;
    if (s.printer !== undefined) printerId = s.printer;
    // Legacy settings files used "nozzle"; newer ones use "nozzleDiameter".
    if (s.nozzleDiameter !== undefined) nozzleDiameter = s.nozzleDiameter;
    else if (s.nozzle !== undefined) nozzleDiameter = s.nozzle;
    if (s.layerHeight !== undefined) layerHeight = s.layerHeight;
    reconcilePrinterSelection();
    if (s.baseColor !== undefined) baseColor = s.baseColor ? { hex: s.baseColor.hex, label: s.baseColor.label || '', collection: s.baseColor.collection || '' } : null;
    if (s.colorSlots !== undefined) {
      colorSlots = s.colorSlots.map((c: any) => c ? { hex: c.hex, label: c.label || '', collection: c.collection || '' } : null);
    }
    if (s.inventoryCollection !== undefined) {
      inventoryCollection = s.inventoryCollection;
      loadInventoryCollectionColors(inventoryCollection);
    }
    if (s.brightness !== undefined) { brightness = s.brightness; committedBrightness = s.brightness; }
    if (s.contrast !== undefined) { contrast = s.contrast; committedContrast = s.contrast; }
    if (s.saturation !== undefined) { saturation = s.saturation; committedSaturation = s.saturation; }
    if (s.warpPins !== undefined) {
      warpPins = s.warpPins.map((p: any) => ({ sourceHex: p.sourceHex, targetHex: p.targetHex, targetLabel: p.targetLabel || '', sigma: p.sigma }));
    }
    if (s.stickers !== undefined) {
      stickers = s.stickers.map((st: any) => ({
        imagePath: st.imagePath,
        fileName: (st.imagePath || '').split(/[/\\]/).pop() || st.imagePath,
        thumbnail: '',
        center: st.center,
        normal: st.normal,
        up: st.up,
        scale: st.scale,
        rotation: st.rotation,
        maxAngle: st.maxAngle ?? 0,
        mode: st.mode === 'projection' ? 'projection' : 'unfold',
      }));
      // Load thumbnails asynchronously.
      stickers.forEach((st, i) => {
        ReadStickerThumbnail(st.imagePath).then(thumb => {
          stickers[i] = { ...stickers[i], thumbnail: thumb };
          stickers = stickers;
        }).catch(() => {});
      });
    }
    if (s.dither !== undefined) dither = s.dither;
    if (s.colorSnap !== undefined) { colorSnap = s.colorSnap; committedColorSnap = s.colorSnap; }
    if (s.noMerge !== undefined) noMerge = s.noMerge;
    if (s.noSimplify !== undefined) noSimplify = s.noSimplify;
    if (s.stats !== undefined) stats = s.stats;
    if (s.alphaWrap !== undefined) alphaWrap = s.alphaWrap;
    if (s.alphaWrapAlpha !== undefined) alphaWrapAlpha = s.alphaWrapAlpha;
    if (s.alphaWrapOffset !== undefined) alphaWrapOffset = s.alphaWrapOffset;
    if (s.splitEnabled !== undefined) splitEnabled = s.splitEnabled;
    if (s.splitAxis !== undefined) splitAxis = s.splitAxis;
    if (s.splitOffset !== undefined) splitOffset = s.splitOffset;
    if (s.splitConnectorStyle !== undefined) splitConnectorStyle = s.splitConnectorStyle;
    if (s.splitConnectorCount !== undefined) splitConnectorCount = s.splitConnectorCount;
    if (s.splitConnectorDiamMM !== undefined) splitConnectorDiamMM = s.splitConnectorDiamMM;
    if (s.splitConnectorDepthMM !== undefined) splitConnectorDepthMM = s.splitConnectorDepthMM;
    if (s.splitClearanceMM !== undefined) splitClearanceMM = s.splitClearanceMM;
    if (s.splitGapMM !== undefined) splitGapMM = s.splitGapMM;
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

  // Load printer registry from Go. Fallback: leave printers empty and the
  // UI will show a minimal select rather than crashing.
  (async () => {
    try {
      const list = await ListPrinters();
      printers = list ?? [];
      // After printers load, ensure nozzle/layer-height are valid for the
      // currently selected printer. This also handles the case where a
      // saved settings file picked a printer we now resolve properly.
      reconcilePrinterSelection();
    } catch (err) {
      log(`ListPrinters failed: ${err}`);
    }
  })();

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
      Printer: printerId,
      InventoryFile: '',
      InventoryColors: invColors,
      InventoryLabels: invLabels,
      Brightness: committedBrightness,
      Contrast: committedContrast,
      Saturation: committedSaturation,
      Dither: dither,
      NoMerge: noMerge,
      NoSimplify: noSimplify,
      AlphaWrap: alphaWrap,
      AlphaWrapAlpha: parseFloat(alphaWrapAlpha) || 0,
      AlphaWrapOffset: parseFloat(alphaWrapOffset) || 0,
      Split: {
        Enabled: splitEnabled,
        Axis: splitAxis,
        Offset: splitOffset,
        ConnectorStyle: splitConnectorStyle,
        ConnectorCount: splitConnectorCount,
        ConnectorDiamMM: splitConnectorDiamMM,
        ConnectorDepthMM: splitConnectorDepthMM,
        ClearanceMM: splitClearanceMM,
        GapMM: splitGapMM,
      },
      Force: force,
      ReloadSeq: reloadSeq,
      ObjectIndex: objectIndex,
      Stats: stats,
      ColorSnap: committedColorSnap,
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
          Mode: s.mode,
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

<Tooltip.Provider>

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
        <Menubar.Separator />
        <Menubar.Item onSelect={Quit}>Exit</Menubar.Item>
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
        <span class="text-xs text-muted-foreground" title={settingsPath || inputFile}>{shortenPath(settingsPath || inputFile)}</span>
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
      <Card.Content class="pt-6 space-y-6">
        <SettingsSection title="Printer">
          {#snippet tip()}
            <HelpTip>
              Target hardware. Sets the smallest detail the output can reproduce.
            </HelpTip>
          {/snippet}
          <div class="grid grid-cols-2 gap-x-4 gap-y-2 items-end">
            <div class="col-span-2 flex items-center gap-1.5">
              <span class="text-sm font-medium">Printer</span>
              <HelpTip>
                Target printer for the exported 3MF. Nozzle and layer height
                options adapt to what the selected printer supports.
              </HelpTip>
            </div>
            <select
              class="col-span-2 h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
              bind:value={printerId}
              onchange={() => reconcilePrinterSelection()}
            >
              {#each printers as p (p.id)}
                <option value={p.id}>{p.displayName}</option>
              {/each}
              {#if printers.length === 0}
                <option value={printerId}>{printerId}</option>
              {/if}
            </select>

            <div class="flex items-center gap-1.5">
              <span class="text-sm font-medium">Nozzle (mm)</span>
              <HelpTip>
                Nozzle diameter variant for the selected printer. Also sets the
                finest horizontal detail the output can represent.
              </HelpTip>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="text-sm font-medium">Layer height (mm)</span>
              <HelpTip>
                Vertical resolution of the print. Must match the layer height
                used when slicing.
              </HelpTip>
            </div>
            <select
              class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
              bind:value={nozzleDiameter}
              onchange={() => reconcilePrinterSelection()}
            >
              {#if currentPrinter}
                {#each currentPrinter.nozzles as n (n.diameter)}
                  <option value={n.diameter}>{n.diameter}</option>
                {/each}
              {:else}
                <option value={nozzleDiameter}>{nozzleDiameter}</option>
              {/if}
            </select>
            <select
              class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
              bind:value={layerHeight}
            >
              {#if currentNozzle}
                {#each currentNozzle.layerHeights as lh (lh)}
                  <option value={fmtLayerHeight(lh)}>{fmtLayerHeight(lh)}</option>
                {/each}
              {:else}
                <option value={layerHeight}>{layerHeight}</option>
              {/if}
            </select>
          </div>
        </SettingsSection>

        <SettingsSection title="Model">
          {#snippet tip()}
            <HelpTip>
              Size the model, set a fallback color, and optionally alpha-wrap to clean up bad geometry.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <div class="grid grid-cols-2 gap-x-4 gap-y-2 items-end">
              <div class="flex items-center gap-3">
                <label class="flex items-center gap-1.5 text-sm font-medium">
                  <input type="radio" name="sizemode" value="size" checked={sizeMode === 'size'} onchange={() => { sizeMode = 'size'; }} />
                  Size (mm)
                </label>
                <label class="flex items-center gap-1.5 text-sm font-medium">
                  <input type="radio" name="sizemode" value="scale" checked={sizeMode === 'scale'} onchange={() => { sizeMode = 'scale'; }} />
                  Scale
                </label>
                <HelpTip>
                  Size sets the longest dimension of the output in millimeters. Scale multiplies the model's native size.
                </HelpTip>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium">Base color</span>
                <HelpTip>
                  Color used for faces that aren't covered by the model's texture. Pick one to override the model's default.
                </HelpTip>
              </div>
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
            </div>

            <!-- Alpha-wrap -->
            <div class="space-y-2">
              <label class="flex items-center gap-2 text-sm font-medium">
                <Checkbox bind:checked={alphaWrap} />
                Alpha-wrap (clean geometry for 3D printing)
                <HelpTip>
                  Wrap the model with a watertight shell to fix self-intersections, thin walls, and other geometry that slicers choke on. Runs after the output is generated and can be slow on large models.
                </HelpTip>
              </label>
              {#if alphaWrap}
                <div class="grid grid-cols-2 gap-3 pl-6 text-sm">
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Alpha (mm)
                      <HelpTip>
                        Radius of the probing sphere. Larger = smoother wrap that bridges gaps but loses detail; smaller = hugs the surface more tightly.
                      </HelpTip>
                    </span>
                    <input type="number" step="0.1" min="0"
                           placeholder={`auto (${(parseFloat(nozzleDiameter) || 0.4).toFixed(2)})`}
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={alphaWrapAlpha} />
                  </label>
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Offset (mm)
                      <HelpTip>
                        How far the wrap sits above the input surface. Larger values shrink-wrap less tightly.
                      </HelpTip>
                    </span>
                    <input type="number" step="0.01" min="0"
                           placeholder="auto (alpha / 30)"
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={alphaWrapOffset} />
                  </label>
                </div>
              {/if}
            </div>
          </div>
        </SettingsSection>

        <SettingsSection title="Split" open={false}>
          {#snippet tip()}
            <HelpTip>
              Cut the model into two halves that print side by side
              and assemble back together with peg/pocket alignment.
              Useful for build-volume limits, or to expose supports
              that would otherwise be hard to remove.
            </HelpTip>
          {/snippet}
          <SplitControls
            bind:enabled={splitEnabled}
            bind:axis={splitAxis}
            bind:offset={splitOffset}
            bind:connectorStyle={splitConnectorStyle}
            bind:connectorCount={splitConnectorCount}
            bind:connectorDiamMM={splitConnectorDiamMM}
            bind:connectorDepthMM={splitConnectorDepthMM}
            bind:clearanceMM={splitClearanceMM}
            bind:gapMM={splitGapMM}
            minOffset={splitOffsetMin}
            maxOffset={splitOffsetMax}
            onAlphaWrapForced={() => { alphaWrap = true; }}
          />
        </SettingsSection>

        <SettingsSection title="Stickers" open={false}>
          {#snippet tip()}
            <HelpTip>
              Stamp logos, labels, or artwork onto the model surface.
            </HelpTip>
          {/snippet}
          <StickerPanel
            bind:stickers={stickers}
            bind:placingIndex={placingStickerIndex}
            onAdd={addSticker}
            onRemove={removeSticker}
          />
        </SettingsSection>

        <SettingsSection title="Color">
          {#snippet tip()}
            <HelpTip>
              Adjust the input texture and tune color mapping before dithering.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <div class="space-y-3">
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-1.5">
                    <Label>Brightness</Label>
                    <HelpTip>
                      Shift the input texture lighter or darker before dithering.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-8 text-right">{brightness}</span>
                </div>
                <Slider type="single" min={-100} max={100} step={1} value={brightness} onValueChange={(v: number) => brightness = v} onValueCommit={(v: number) => committedBrightness = v} />
              </div>
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-1.5">
                    <Label>Contrast</Label>
                    <HelpTip>
                      Stretch or compress the tonal range of the input texture before dithering.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-8 text-right">{contrast}</span>
                </div>
                <Slider type="single" min={-100} max={100} step={1} value={contrast} onValueChange={(v: number) => contrast = v} onValueCommit={(v: number) => committedContrast = v} />
              </div>
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-1.5">
                    <Label>Saturation</Label>
                    <HelpTip>
                      Make colors more vivid or closer to gray before dithering.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-8 text-right">{saturation}</span>
                </div>
                <Slider type="single" min={-100} max={100} step={1} value={saturation} onValueChange={(v: number) => saturation = v} onValueCommit={(v: number) => committedSaturation = v} />
              </div>
            </div>

            <ColorPinEditor
              bind:pins={warpPins}
              loadCollectionColors={GetCollectionColors}
              bind:pickingIndex={pickingPinIndex}
              onStartPick={(i: number) => pickingPinIndex = pickingPinIndex === i ? -1 : i}
            />

            <!-- Color snap -->
            <div class="space-y-1">
              <div class="flex items-center justify-between">
                <div class="flex items-center gap-1.5">
                  <Label>Color snap (delta E)</Label>
                  <HelpTip>
                    CIELAB distance below which pixels snap to the nearest palette color instead of being dithered. Lower values preserve more color detail; higher values reduce dithering artifacts.
                  </HelpTip>
                </div>
                <span class="text-xs text-muted-foreground w-8 text-right">{colorSnap}</span>
              </div>
              <Slider type="single" min={0} max={50} step={1} value={colorSnap} onValueChange={(v: number) => colorSnap = v} onValueCommit={(v: number) => committedColorSnap = v} />
            </div>
          </div>
        </SettingsSection>

        <SettingsSection title="Filament">
          {#snippet tip()}
            <HelpTip>
              Filament slots used in the output. Click a slot to lock it to a specific color; unlocked slots are filled automatically from the chosen collection. Use + to add more slots.
            </HelpTip>
          {/snippet}
          <div class="space-y-4">
            <!-- Color palette grid -->
            <div class="space-y-2">
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
              <div class="flex items-center gap-1.5">
                <Label>Unlocked colors from</Label>
                <HelpTip>
                  Filament collection the auto-picker draws from for unlocked palette slots. Manage collections from the Filaments menu.
                </HelpTip>
              </div>
              <CollectionSelect
                bind:selected={inventoryCollection}
                onchange={loadInventoryCollectionColors}
              />
            </div>
          </div>
        </SettingsSection>

        <SettingsSection title="Advanced" open={false}>
          {#snippet tip()}
            <HelpTip>
              Dither algorithm and diagnostic toggles. Most users can ignore these.
            </HelpTip>
          {/snippet}
          <div class="space-y-4">
            <div class="space-y-2">
              <div class="flex items-center gap-1.5">
                <Label for="dither">Dither mode</Label>
                <HelpTip>
                  Algorithm used to blend palette colors across the surface. "dizzy" is the default ordered dither; "none" disables dithering entirely.
                </HelpTip>
              </div>
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

            <div class="flex flex-wrap gap-x-6 gap-y-3">
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={noMerge} />
                No merge
                <HelpTip>
                  Skip merging adjacent same-color voxels into larger regions. Produces more primitives but can preserve fine dither detail.
                </HelpTip>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={noSimplify} />
                No simplify
                <HelpTip>
                  Skip mesh simplification. Keeps the raw per-voxel geometry, which is accurate but dramatically larger.
                </HelpTip>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <Checkbox bind:checked={stats} />
                Stats
                <HelpTip>
                  Log summary statistics (triangle counts, color usage, timings) to the terminal.
                </HelpTip>
              </label>
            </div>
          </div>
        </SettingsSection>
      </Card.Content>
    </Card.Root>

    {#if statusMessage}
    <div class="mt-4">
      <p class="text-sm {statusType === 'success' ? 'text-green-500' : statusType === 'error' ? 'text-red-500' : statusType === 'warning' ? 'text-yellow-500' : 'text-muted-foreground'}">
        {statusMessage}
      </p>
    </div>
    {/if}

    </div>
  </div>

  <!-- Right column: 3D viewers -->
  <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
    <div class="flex-1 min-h-0">
      <ModelViewer meshUrl={inputMeshUrl} overlayMeshUrl={inputOverlayMeshUrl} label={inputFile ? `Input Model: ${shortenPath(inputFile)}` : 'Input Model'} viewerId="input" camera={sharedCamera} {brightness} {contrast} {saturation} pickMode={pickingPinIndex >= 0} stickerPlaceMode={placingStickerIndex >= 0} stickerImage={placingSticker?.thumbnail ?? ''} stickerSize={(placingSticker?.scale ?? 0) * (calibratedPreviewScale ?? 1)} stickerRotation={placingSticker?.rotation ?? 0} onColorPick={handleColorPick} onStickerPlace={handleStickerPlace} warpPins={pickingPinIndex >= 0 ? [] : warpPins} loading={inputFile ? inputFile.split('/').pop() ?? '' : ''} errorMessage={inputError} cutPlane={cutPlanePreview} />
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

</Tooltip.Provider>
