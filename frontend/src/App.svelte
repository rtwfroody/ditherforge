<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import * as Card from '$lib/components/ui/card';
  import * as AlertDialog from '$lib/components/ui/alert-dialog';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Slider } from '$lib/components/ui/slider';
  import * as Tooltip from '$lib/components/ui/tooltip';
  import HelpTip from '$lib/components/HelpTip.svelte';
  import SettingsSection from '$lib/components/SettingsSection.svelte';
  import SplitControls from '$lib/components/SplitControls.svelte';
  import { LockIcon, LockOpenIcon, LoaderCircleIcon, SunIcon, MoonIcon, ChevronDownIcon } from '@lucide/svelte';
  import * as Menubar from '$lib/components/ui/menubar';
  import ModelViewer from '$lib/components/ModelViewer.svelte';
  import CollectionPicker from '$lib/components/CollectionPicker.svelte';
  import CollectionSelect from '$lib/components/CollectionSelect.svelte';
  import ColorPinEditor from '$lib/components/ColorPinEditor.svelte';
  import CollectionManager from '$lib/components/CollectionManager.svelte';
  import StickerPanel from '$lib/components/StickerPanel.svelte';
  import ObjectPicker from '$lib/components/ObjectPicker.svelte';
  import DebugCellsDialog from '$lib/components/DebugCellsDialog.svelte';
  import TriangleInfoDialog from '$lib/components/TriangleInfoDialog.svelte';
  import CellInfoDialog from '$lib/components/CellInfoDialog.svelte';
  import type { StickerUI } from '$lib/components/StickerPanel.svelte';
  import { SharedCamera } from '$lib/components/SharedCamera.svelte';
  import { contrastColor } from '$lib/utils';
  import type { CutPlanePreview } from '$lib/types';
  import {
    SPLIT_ORIENTATION_VALUES,
    SPLIT_CONNECTOR_VALUES,
    SPLIT_AXIS_VALUES,
    SPLIT_AXIS_OPTIONS,
    DITHER_OPTIONS,
    DITHER_VALUES,
    SIZE_MODE_VALUES,
    BASE_COLOR_MODE_VALUES,
    STICKER_MODE_VALUES,
    type SplitOrientation,
    type SplitConnectorStyle,
    type SplitAxis,
    type DitherMode,
    type SizeMode,
    type BaseColorMode,
  } from '$lib/settingsOptions';
  import { ProcessPipeline, Export3MF, SaveSettings, SaveSettingsDialog, OpenFileDialog, OpenModelDialog, LoadSettingsFile, DefaultSettingsPath, Version, LogMessage, GetCollectionColors, ImportCollection, ExportCollection, CreateCollection, DeleteCollection, OpenStickerImage, ReadStickerThumbnail, OpenMaterialXFile, ValidateMaterialX, EnumerateObjects, ListPrinters, SelectCellDiagnostics, DitherModePreviews, Quit } from '../wailsjs/go/main/App';
  import type { main } from '../wailsjs/go/models';
  import { collectionStore } from '$lib/stores/collections.svelte';
  import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';
  import type { loader, settings, pipeline } from '../wailsjs/go/models';

  // Dither-mode preview thumbnails (rendered offline by cmd/dither-thumbs
  // with the real internal/voxel dither implementations). Vite resolves each
  // import to a hashed static-asset URL at build time.
  import thumbDLCd30p7 from './assets/dither/dlc-d30-p7.png';
  import thumbFloydSteinberg from './assets/dither/floyd-steinberg.png';
  import thumbRiemersma from './assets/dither/riemersma.png';
  import thumbBNAdapt5 from './assets/dither/bn-adapt-5.png';
  import thumbNone from './assets/dither/none.png';

  // Per-mode thumbnail + ≤6-word tagline for the visual picker, keyed by the
  // DITHER_OPTIONS value (the exact string persisted to settings JSON). Labels
  // still come from DITHER_OPTIONS.
  const DITHER_META: Record<string, { thumb: string; tagline: string }> = {
    'dlc-d30-p7':      { thumb: thumbDLCd30p7,       tagline: 'iterated, less drift'  },
    'floyd-steinberg': { thumb: thumbFloydSteinberg, tagline: 'classic, slight banding' },
    'riemersma':       { thumb: thumbRiemersma,      tagline: 'organic, no direction' },
    'bn-adapt-5':      { thumb: thumbBNAdapt5,       tagline: 'even grain, bounded'   },
    'none':            { thumb: thumbNone,           tagline: 'nearest color only'    },
  };

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
  let sizeMode: SizeMode = $state('size');
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
  // MaterialX base color. Path is read by the backend at pipeline run
  // time; settings round-trips the path. Accepts .mtlx (with adjacent
  // textures) or a .zip containing both.
  let baseMaterialXPath = $state<string>('');
  // Stored as a fraction of the model's max extent (was 10 mm; 0.1 ≈ a 10 mm
  // tile on a 100 mm model). The "Tile size" UI shows it back in mm.
  let baseMaterialXTileMM = $state<number>(0.1);
  let baseMaterialXTriplanarSharpness = $state<number>(4);
  // Persistent inline error for the currently-selected MaterialX file.
  // Set by validateMaterialXFile (file-pick / settings-load) and by the
  // pipeline-warning event handler when the running pipeline reports a
  // MaterialX failure. Cleared whenever baseMaterialXPath changes.
  // Surfaced as a red banner directly under the file display so the
  // user can't miss "this texture isn't actually being applied" — the
  // status-bar warning by itself was easy to overlook.
  let baseMaterialXError = $state<string>('');
  // baseColorMode picks which of the two pickers (and the
  // corresponding pipeline option) is in effect. Backend only ever
  // gets one — the other is suppressed.
  let baseColorMode = $state<BaseColorMode>('solid');
  // Color palette: each slot is either null (auto) or a locked color with hex + label + source collection.
  type ColorInfo = { hex: string; label: string; collection?: string; td?: number };
  type ColorSlot = ColorInfo | null;
  let colorSlots = $state<ColorSlot[]>([null, null, null, null]);
  let pickerIndex = $state<number | null>(null);
  // For collection-based inventory source:
  let inventoryCollection = $state('Inventory');
  let inventoryCollectionColors = $state<{ hex: string; label: string; td: number }[]>([]);
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
  let dither = $state('dlc-d30-p7');
  let riemersmaBias = $state(0.85);
  let committedRiemersmaBias = $state(0.85);
  let blueNoiseTol = $state(20);
  let committedBlueNoiseTol = $state(20);
  let colorSnap = $state(5);
  let committedColorSnap = $state(5);
  let noMerge = $state(false);
  let noCellMerge = $state(false);
  let noSimplify = $state(false);
  let honorTD = $state(true);
  // Translucency model: '' (area compensation, legacy) or 'layered' (infill-aware).
  let tdModel = $state('');
  let infillColor = $state('#FFFFFF');
  let colorAwareCells = $state(true);
  let colorRegionContrast = $state(20);
  let committedColorRegionContrast = $state(20);
  // Advanced: confine the dither to colour regions so a grey area's
  // diffused error can't bleed into an adjacent solid black/white area.
  let regionDither = $state(false);
  let regionDitherDeltaE = $state(20);
  let committedRegionDitherDeltaE = $state(20);
  let rejectColorOutliers = $state(true);
  let stats = $state(false);
  // Debug: when true, the output mesh is colored by each face's
  // originating section's raw sampled RGB instead of the dithered
  // palette. Lets us visually distinguish sampling bugs from
  // dither / mesh-emission bugs in the GUI viewer.
  let showSampledColors = $state(false);
  // Mesh-repair method: 'none' | 'fwn' (winding-number remesh) |
  // 'alphawrap' (CGAL alpha wrap). Persisted as meshRepair; the Go
  // backend migrates legacy alphaWrap:true files to 'alphawrap'.
  const MESH_REPAIR_VALUES = ['none', 'fwn', 'alphawrap'] as const;
  // Typed as plain string (not the union) on purpose: a union-typed $state
  // read in a script-level $derived (modelSummary) narrows to its
  // initializer literal 'none', which makes svelte-check flag the
  // `meshRepair !== 'none'` comparison as always-false. String sidesteps it.
  let meshRepair = $state('none');
  let alphaWrapAlpha = $state('');   // mm; '' = auto (nozzle diameter). Alpha-wrap probe radius only.
  let alphaWrapOffset = $state('');  // mm; '' = auto (alpha / 30). Alpha-wrap surface offset only.
  // FWN remesh per-axis grid-pitch overrides (mm; '' = auto — XY from nozzle
  // diameter, Z from layer height). Consulted only in fwn mode.
  let fwnDetailXY = $state('');
  let fwnDetailZ = $state('');
  // Layer-0 voxel-XY multiplier for bed adhesion. 1 = no enlargement;
  // higher = bigger first-layer color blobs that stick better.
  // Backend treats 0/negative as "use built-in default".
  let layer0AdhesionXYScale = $state(2);
  let committedLayer0AdhesionXYScale = $state(2);
  // Upper-layer voxel-XY multiplier relative to the slicer line width.
  // 1 = unchanged; <1 = finer color detail at the cost of more
  // primitives; >1 = coarser. Backend treats 0/negative as "use
  // built-in default" (1).
  let upperLayerXYScale = $state(1.25);
  let committedUpperLayerXYScale = $state(1.25);
  // Split (cut model into two halves with peg/pocket connectors).
  // See docs/SPLIT.md. Defaults match the design doc's "what most
  // users want" baseline.
  let splitEnabled = $state(false);
  let splitAxis = $state<SplitAxis>(2);
  let splitOffset = $state(0);
  // Tilt of the cut plane, in degrees, about its two in-plane axes.
  // 0/0 = axis-aligned (the legacy behaviour). See SplitControls.svelte.
  let splitTiltA = $state(0);
  let splitTiltB = $state(0);
  let splitConnectorStyle = $state<SplitConnectorStyle>('pegs');
  let splitConnectorCount = $state(0); // 0 = auto
  let splitConnectorDiamMM = $state(3);
  let splitConnectorDepthMM = $state(2);
  let splitClearanceMM = $state(0.15);
  // Per-half orientation: which model axis points up. Defaults to
  // "z-up" (the model's authored orientation). The user picks per half
  // independently. See SplitControls.svelte for option semantics.
  let splitOrientationA = $state<SplitOrientation>('z-up');
  let splitOrientationB = $state<SplitOrientation>('z-up');
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
  // splitOffset is a fraction of the print extent, while the bbox is in
  // mm, so compare/convert via scaledMaxExtentMM. splitOffset is read and
  // written inside untrack so this fires only on axis/bbox changes, not on
  // its own write.
  $effect(() => {
    const axis = splitAxis;
    if (!modelBBoxMin || !modelBBoxMax) return;
    const lo = modelBBoxMin[axis];
    const hi = modelBBoxMax[axis];
    untrack(() => {
      const ext = scaledMaxExtentMM;
      if (!ext || ext <= 0) return;
      const offMM = splitOffset * ext;
      if (offMM < lo || offMM > hi) {
        splitOffset = ((lo + hi) / 2) / ext;
      }
    });
  });

  // Tilt-plane geometry, mirroring internal/split/split.go's AxisBasis
  // and TiltedFrame. Kept in sync with the Go so the preview quad and
  // the real cut agree. Pure helpers — no reactive reads.
  type Vec3 = [number, number, number];
  function axisBasis(axis: number): { u: Vec3; v: Vec3 } {
    switch (axis) {
      case 0: return { u: [0, 1, 0], v: [0, 0, 1] };
      case 1: return { u: [0, 0, 1], v: [1, 0, 0] };
      default: return { u: [1, 0, 0], v: [0, 1, 0] };
    }
  }
  function rotAboutAxis(vec: Vec3, ax: Vec3, theta: number): Vec3 {
    const c = Math.cos(theta), s = Math.sin(theta);
    const cross: Vec3 = [
      ax[1] * vec[2] - ax[2] * vec[1],
      ax[2] * vec[0] - ax[0] * vec[2],
      ax[0] * vec[1] - ax[1] * vec[0],
    ];
    const d = (ax[0] * vec[0] + ax[1] * vec[1] + ax[2] * vec[2]) * (1 - c);
    return [
      vec[0] * c + cross[0] * s + ax[0] * d,
      vec[1] * c + cross[1] * s + ax[1] * d,
      vec[2] * c + cross[2] * s + ax[2] * d,
    ];
  }
  // Normal + right-handed in-plane basis for `axis` tilted by aDeg about
  // the base U axis, then bDeg about the resulting V axis. 0/0 = aligned.
  function tiltedFrame(axis: number, aDeg: number, bDeg: number): { normal: Vec3; u: Vec3; v: Vec3 } {
    const a = axis < 0 || axis > 2 ? 2 : axis;
    let normal: Vec3 = [0, 0, 0];
    normal[a] = 1;
    let { u, v } = axisBasis(a);
    if (aDeg === 0 && bDeg === 0) return { normal, u, v };
    const ar = (aDeg * Math.PI) / 180, br = (bDeg * Math.PI) / 180;
    normal = rotAboutAxis(normal, u, ar);
    v = rotAboutAxis(v, u, ar);
    normal = rotAboutAxis(normal, v, br);
    u = rotAboutAxis(u, v, br);
    return { normal, u, v };
  }

  // Cut-plane preview overlay for the input viewer. Mirrors the
  // backend's pipeline.computeSplitPreviewFromVertices in
  // internal/pipeline/splitpreview.go — keep the two in sync. The
  // (U, V) basis is right-handed with U × V = Normal, and the quad
  // is centred on the model's bbox so it sits symmetrically over the
  // mesh. Computed client-side from the bbox so it tracks the slider
  // without RPC churn.
  //
  // The plane may be tilted off the principal axis by splitTiltA/B, so
  // (U, V) are not axis-aligned in general. We therefore project all
  // eight bbox corners onto the tilted basis instead of two opposite
  // corners. The pivot (cut position) is still computed on the
  // axis-aligned base basis, exactly mirroring splitPlanePivot in
  // splitpreview.go so the rendered quad registers with the real cut.
  const cutPlanePreview = $derived.by((): CutPlanePreview | null => {
    if (!splitEnabled || !modelBBoxMin || !modelBBoxMax) return null;
    const axis = splitAxis;
    const { normal, u, v } = tiltedFrame(axis, splitTiltA, splitTiltB);
    const { u: u0, v: v0 } = axisBasis(axis);
    const proj = (p: Vec3, a: Vec3) => p[0] * a[0] + p[1] * a[1] + p[2] * a[2];

    // Eight bbox corners.
    const lo = modelBBoxMin, hi = modelBBoxMax;
    const corners: Vec3[] = [];
    for (const x of [lo[0], hi[0]])
      for (const y of [lo[1], hi[1]])
        for (const z of [lo[2], hi[2]]) corners.push([x, y, z]);

    // Pivot: silhouette centre on the base (axis-aligned) basis, placed
    // at the cut offset along the axis. splitOffset is a fraction of the
    // print extent; convert to pipeline-mm (the bbox frame) first.
    let minU0 = Infinity, maxU0 = -Infinity, minV0 = Infinity, maxV0 = -Infinity;
    for (const c of corners) {
      const du = proj(c, u0), dv = proj(c, v0);
      minU0 = Math.min(minU0, du); maxU0 = Math.max(maxU0, du);
      minV0 = Math.min(minV0, dv); maxV0 = Math.max(maxV0, dv);
    }
    const cu = (minU0 + maxU0) / 2, cv = (minV0 + maxV0) / 2;
    const offAxis = splitOffset * (scaledMaxExtentMM ?? 0);
    const n0: Vec3 = [0, 0, 0];
    n0[axis] = 1;
    const pivot: Vec3 = [
      offAxis * n0[0] + cu * u0[0] + cv * v0[0],
      offAxis * n0[1] + cu * u0[1] + cv * v0[1],
      offAxis * n0[2] + cu * u0[2] + cv * v0[2],
    ];

    // Half-extents symmetric about the pivot along the tilted (U, V).
    let halfU = 0, halfV = 0;
    for (const c of corners) {
      const d: Vec3 = [c[0] - pivot[0], c[1] - pivot[1], c[2] - pivot[2]];
      halfU = Math.max(halfU, Math.abs(proj(d, u)));
      halfV = Math.max(halfV, Math.abs(proj(d, v)));
    }

    // The bbox is in original-mesh mm but the input viewer renders the
    // mesh at previewScale (vertices multiplied by previewScale in
    // scalePreviewMesh). Scale origin and half-extents to match the
    // rendered frame; (u, v, normal) directions are scale-invariant.
    const ps = previewScale;
    return {
      origin: [pivot[0] * ps, pivot[1] * ps, pivot[2] * ps],
      normal,
      u,
      v,
      halfExtentU: halfU * ps,
      halfExtentV: halfV * ps,
    };
  });

  // Cascade: turning mesh repair off while Split is on auto-disables
  // Split (the cut needs a watertight input). The reverse cascade
  // (turning Split on auto-enables a repair mode) lives in
  // SplitControls.svelte's onRepairForced callback.
  $effect(() => {
    if (meshRepair === 'none' && splitEnabled) {
      splitEnabled = false;
    }
  });
  let stickers = $state<StickerUI[]>([]);
  let placingStickerIndex = $state(-1);
  const placingSticker = $derived(placingStickerIndex >= 0 ? stickers[placingStickerIndex] ?? null : null);

  // Recent files (persisted in localStorage). Input models and settings JSON
  // are now picked through separate menus/controls, so each keeps its own
  // recent list rather than sharing one mixed list.
  const MAX_RECENT = 10;
  function loadRecentList(key: string): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(key) || '[]');
      return Array.isArray(raw) ? raw.filter((x: unknown) => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }

  // One-time migration: the old combined 'recentFiles' list mixed models and
  // settings. Split it by extension into the two new lists, then drop it.
  (function migrateRecentFiles() {
    const legacy = localStorage.getItem('recentFiles');
    if (legacy === null) return;
    if (localStorage.getItem('recentModels') === null && localStorage.getItem('recentSettings') === null) {
      const all = loadRecentList('recentFiles');
      const models = all.filter(p => p.split('.').pop()?.toLowerCase() !== 'json').slice(0, MAX_RECENT);
      const settings = all.filter(p => p.split('.').pop()?.toLowerCase() === 'json').slice(0, MAX_RECENT);
      localStorage.setItem('recentModels', JSON.stringify(models));
      localStorage.setItem('recentSettings', JSON.stringify(settings));
    }
    localStorage.removeItem('recentFiles');
  })();

  let recentModels = $state<string[]>(loadRecentList('recentModels'));
  let recentSettings = $state<string[]>(loadRecentList('recentSettings'));

  function addRecentModel(path: string) {
    recentModels = [path, ...recentModels.filter(p => p !== path)].slice(0, MAX_RECENT);
    localStorage.setItem('recentModels', JSON.stringify(recentModels));
  }

  function removeRecentModel(path: string) {
    recentModels = recentModels.filter(p => p !== path);
    localStorage.setItem('recentModels', JSON.stringify(recentModels));
  }

  function addRecentSettings(path: string) {
    recentSettings = [path, ...recentSettings.filter(p => p !== path)].slice(0, MAX_RECENT);
    localStorage.setItem('recentSettings', JSON.stringify(recentSettings));
  }

  function removeRecentSettings(path: string) {
    recentSettings = recentSettings.filter(p => p !== path);
    localStorage.setItem('recentSettings', JSON.stringify(recentSettings));
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

  function pickColor(hex: string, label: string, collection: string, td: number) {
    if (pickerIndex === null) return;
    colorSlots[pickerIndex] = { hex, label, collection, td };
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
  let statusMessage = $state('');
  let statusType: 'idle' | 'success' | 'error' | 'warning' = $state('idle');
  let version = $state('');
  let forceDialogOpen = $state(false);
  let forceExtentMM = $state(0);
  let debugCellsDialogOpen = $state(false);
  let triangleSelectMode = $state(false);
  let triangleInfoDialogOpen = $state(false);
  let pickedTriangle = $state<null | {
    viewerId: string;
    viewerLabel: string;
    faceIndex: number;
    vertices: [
      [number, number, number],
      [number, number, number],
      [number, number, number],
    ];
  }>(null);
  function handleTrianglePick(hit: {
    viewerId: string;
    viewerLabel: string;
    faceIndex: number;
    vertices: [
      [number, number, number],
      [number, number, number],
      [number, number, number],
    ];
  }) {
    pickedTriangle = hit;
    triangleSelectMode = false;
    triangleInfoDialogOpen = true;
  }

  let cellSelectMode = $state(false);
  let cellInfoDialogOpen = $state(false);
  let cellInfoLoading = $state(false);
  let cellInfoError = $state('');
  let cellInfo = $state<pipeline.CellDiagInfo | null>(null);
  async function handleCellPick(hit: {
    viewerId: string;
    viewerLabel: string;
    faceIndex: number;
    point: [number, number, number];
  }) {
    cellSelectMode = false;
    cellInfo = null;
    cellInfoError = '';
    cellInfoLoading = true;
    cellInfoDialogOpen = true;
    try {
      cellInfo = await SelectCellDiagnostics(hit.point[0], hit.point[1], hit.point[2]);
    } catch (e) {
      cellInfoError = String(e);
    } finally {
      cellInfoLoading = false;
    }
  }

  // Binary mesh URLs for the 3D viewers live inside the single `run`
  // record declared below (run.input.*, run.output.*) and are read here
  // through the $derived aliases (inputMeshUrl, wrappedMeshUrl, …). See
  // the RunView block for the rationale.
  //
  // 'input' = show the textured input mesh; 'wrapped' = show the
  // alpha-wrapped geometry. Toggle disabled when alpha-wrap is off. This
  // is view-selection UI state, not per-run data, so it stays separate.
  let inputViewMode: 'input' | 'wrapped' = $state('input');
  // Overlay "View" popup state for the Input Model viewer.
  let viewMenuOpen = $state(false);
  let viewMenuRef: HTMLDivElement | undefined = $state(undefined);
  let outputViewMenuOpen = $state(false);
  let outputViewMenuRef: HTMLDivElement | undefined = $state(undefined);
  type ViewMode = 'solid' | 'hidden-line';
  let inputViewStyle: ViewMode = $state('solid');
  let outputViewStyle: ViewMode = $state('solid');
  function handleViewMenuOutside(e: MouseEvent) {
    const target = e.target instanceof Node ? e.target : null;
    if (viewMenuOpen && viewMenuRef && target && !viewMenuRef.contains(target)) {
      viewMenuOpen = false;
    }
    if (outputViewMenuOpen && outputViewMenuRef && target && !outputViewMenuRef.contains(target)) {
      outputViewMenuOpen = false;
    }
  }
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
    // Stage was replayed from the disk cache (blob decode rather than a
    // recompute). Set true when a 'cached' event arrives; the "(cache)"
    // label persists through 'done'.
    cached: boolean;
    startedAt: number;  // Date.now() when stage started
    elapsed: number;    // final elapsed seconds (set on done)
    // Date.now() of the last backend signal for this stage (start,
    // progress, or heartbeat). The backend guarantees a liveness tick
    // at least every ~500ms for running stages (progress.Monitor), so
    // a running stage with no signal for a couple of seconds is
    // genuinely stalled — ModelViewer renders it amber.
    lastBeatAt: number;
  };
  // Single source of truth for everything the two model viewers show for
  // one pipeline run. Previously this was ~9 independent mutable $state
  // variables (inputMeshUrl, wrappedMeshUrl, outputMeshUrl, running,
  // pipelineStages, pipelineError, latestGen, …) written from a dozen
  // event handlers, each of which had to remember to reset every variable
  // and re-implement the staleness gate by hand. That made stale writes
  // structurally possible (see docs/output-viewer-run-state-plan.md).
  //
  // Now one record IS the state. Each run owns a monotonic id allocated
  // synchronously on the frontend (runId) before the backend is told about
  // it, so the gate is an exact match (event.gen === run.id) with no
  // lagging async copy to race against. Starting a run — or abandoning one
  // via clearViewportMesh — replaces the record atomically, so there are
  // no scattered per-variable resets to forget.
  type RunPhase = 'idle' | 'running' | 'done' | 'error';
  type RunView = {
    id: number;                    // the owning runId; backend echoes it on every event
    phase: RunPhase;
    stages: StageInfo[];
    error: string;                 // pipeline stage failure text, shown below the stage list
    input: {
      meshUrl?: string;            // textured input mesh
      overlayUrl?: string;         // alpha-wrap sticker overlay (decals when alpha-wrap on)
      wrappedUrl?: string;         // untextured alpha-wrapped geometry preview
    };
    output: {
      // Flat-grey mid-pipeline snapshot (after decimation, alpha-wrap,
      // split) so the Output viewer shows the shape before the final
      // coloured mesh; the viewer prefers finalUrl and falls back to this.
      previewUrl?: string;
      finalUrl?: string;           // final coloured output mesh
      // Per-palette-color usage from the finished mesh (read-only run
      // feedback). Empty/undefined until this run's pipeline-done arrives;
      // reset atomically with the record on the next run.
      colorUsage?: ColorUsage[];
    };
  };
  // Per-color output usage reported by the backend on pipeline-done. Palette
  // order = locked slots first, then auto (same as the palette-resolved event).
  type ColorUsage = { paletteIndex: number; hex: string; triangles: number };
  function idleRun(id: number): RunView {
    return { id, phase: 'idle', stages: [], error: '', input: {}, output: {} };
  }
  // Monotonic run-id counter. Bumped synchronously (no await) whenever a
  // run is started or abandoned, so in-flight events for a superseded run
  // can never satisfy the exact-match gate.
  let runId = 0;
  let run = $state<RunView>(idleRun(0));

  // Derived aliases so the template and event handlers read the same
  // names as before. The display is derived from `run`, never pushed.
  const running = $derived(run.phase === 'running');
  const inputMeshUrl = $derived(run.input.meshUrl);
  const inputOverlayMeshUrl = $derived(run.input.overlayUrl);
  const wrappedMeshUrl = $derived(run.input.wrappedUrl);
  const outputMeshUrl = $derived(run.output.finalUrl);          // final mesh only (export, picking)
  const outputMesh = $derived(run.output.finalUrl ?? run.output.previewUrl); // what the viewer shows
  const pipelineStages = $derived(run.stages);
  const pipelineError = $derived(run.error);

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

  // ---- Dither-mode picker: live per-model preview thumbnails --------------
  //
  // The picker cards default to the committed static PNGs (DITHER_META). Once a
  // model is loaded we snapshot the input viewer, send it to the read-only
  // DitherModePreviews backend endpoint (which runs the real dither code in
  // image space), and swap the mode cards to the returned per-mode PNGs. This is
  // purely presentational — it never touches settings, the pipeline, or the
  // cache. Before a model loads (or on any error) ditherThumbs stays null and
  // the static fallbacks show.

  // Capture function handed up from the input ModelViewer's WebGL context.
  let inputCapture = $state<(() => string | null) | null>(null);
  // Per-mode preview PNGs (mode value -> data URL), or null to use fallbacks.
  let ditherThumbs = $state<Record<string, string> | null>(null);
  // Monotonic request token: latest wins, in-flight stragglers are discarded.
  // Plain variable (not $state) so touching it never schedules reactivity.
  let ditherReqSeq = 0;
  // Last palette a preview was rendered with. Auto slot colors are wiped at
  // run start and only re-resolve after the palette stage (post-voxelize);
  // falling back to this lets the preview refresh immediately on settings
  // changes instead of waiting out voxelize. Plain variable: preview-only,
  // nothing renders it.
  let lastPreviewPalette: string[] = [];

  // Draw a data-URL image onto a wxh canvas with a centered cover crop and
  // return the result as a PNG data URL. ~2x the card size; CSS scales down.
  function coverCropToPNG(dataUrl: string, w: number, h: number): Promise<string> {
    return new Promise((resolve, reject) => {
      const img = new Image();
      img.onload = () => {
        const canvas = document.createElement('canvas');
        canvas.width = w;
        canvas.height = h;
        const cctx = canvas.getContext('2d');
        if (!cctx) { reject(new Error('no 2d context')); return; }
        const scale = Math.max(w / img.width, h / img.height);
        const dw = img.width * scale, dh = img.height * scale;
        cctx.drawImage(img, (w - dw) / 2, (h - dh) / 2, dw, dh);
        resolve(canvas.toDataURL('image/png'));
      };
      img.onerror = () => reject(new Error('snapshot image failed to load'));
      img.src = dataUrl;
    });
  }

  // Build the palette to preview against: locked slot colors plus resolved
  // auto colors, skipping empty/unresolved slots.
  function previewPalette(): string[] {
    return colorSlots
      .map((slot, i) => slot?.hex ?? resolvedBySlot[i]?.hex)
      .filter((h): h is string => !!h);
  }

  async function recomputeDitherThumbs(seq: number) {
    const cap = inputCapture;
    // No model / no live context -> fall back to static thumbnails.
    if (!inputMeshUrl || !cap) {
      if (seq === ditherReqSeq) ditherThumbs = null;
      return;
    }
    let pal = previewPalette();
    if (pal.length < 2) pal = lastPreviewPalette;
    if (pal.length < 2) {
      if (seq === ditherReqSeq) ditherThumbs = null;
      return;
    }
    lastPreviewPalette = pal;
    const full = cap();
    if (!full) {
      if (seq === ditherReqSeq) ditherThumbs = null;
      return;
    }
    try {
      // Render at 288x192 (3:2) — above the picker cards' display size so
      // they stay crisp on hi-dpi. committedColorSnap feeds the real
      // voxel.SnapColors transform; brightness/contrast/saturation and color
      // pins are already baked into the snapshot by the viewer shader.
      const src = await coverCropToPNG(full, 288, 192);
      if (seq !== ditherReqSeq) return; // superseded while cropping
      const res = await DitherModePreviews(src, pal, committedRiemersmaBias, committedBlueNoiseTol, committedColorSnap);
      if (seq === ditherReqSeq) ditherThumbs = res;
    } catch (e) {
      if (seq === ditherReqSeq) ditherThumbs = null;
      LogMessage('info', `dither preview failed: ${e}`);
    }
  }

  // Recompute on: model load, palette change, tuning-slider commit,
  // color-correction change (brightness/contrast/saturation/pins), or the
  // capture function (re)mounting. Debounced ~300ms; NOT triggered by camera
  // movement (we read no camera state here). The effect only reads reactive
  // deps and writes ditherThumbs asynchronously (never reads it), so there is
  // no self-referential $state read/write.
  $effect(() => {
    // Establish reactive dependencies.
    void inputMeshUrl;
    void inputCapture;
    void committedRiemersmaBias;
    void committedBlueNoiseTol;
    void committedColorSnap;
    for (const s of colorSlots) void s?.hex;
    for (const r of resolvedBySlot) void r?.hex;
    // Color correction is baked into the snapshot by the input viewer's
    // shader, so these must retrigger a capture even though none of the
    // backend call's arguments change.
    void brightness;
    void contrast;
    void saturation;
    for (const p of warpPins) {
      void p.sourceHex;
      void p.targetHex;
      void p.sigma;
    }

    const seq = ++ditherReqSeq;
    const handle = window.setTimeout(() => { void recomputeDitherThumbs(seq); }, 300);
    return () => window.clearTimeout(handle);
  });

  // Shared camera state — single source of truth for both viewers.
  const sharedCamera = new SharedCamera();

  // View-change trigger: regenerate the previews once the camera has been
  // stable for 2s. sharedCamera.generation bumps on every orbit/zoom/pan of
  // either viewer (both drive the shared camera, so either changes the input
  // viewer's rendered view). The cleanup clears the pending timer, so each
  // camera change resets the 2s countdown — it only fires after motion stops.
  // Separate from the 300ms effect above (which handles model/palette/tuning);
  // both share ditherReqSeq so latest-wins still holds. Reads only
  // sharedCamera.generation and writes no state synchronously (the timer's
  // async write targets ditherThumbs, which this effect never reads), so the
  // Svelte 5 read/write-same-state rule is respected. The effect-scoped timer
  // is cleared on teardown, so nothing leaks.
  $effect(() => {
    void sharedCamera.generation;
    const seq = ++ditherReqSeq;
    const handle = window.setTimeout(() => { void recomputeDitherThumbs(seq); }, 2000);
    return () => window.clearTimeout(handle);
  });

  // Auto-processing state (plain variables, not reactive -- nothing in the template reads these).
  let processTimer: number | undefined;

  // Every backend event carries the gen (= runId) of the run that emitted
  // it. An event belongs to the current viewer state iff event.gen ===
  // run.id; anything else is from a superseded or torn-down run and is
  // ignored. Because run.id is allocated synchronously before the backend
  // is told about the run, this gate is never stale — there is no window.

  Version().then(v => version = v);

  // Listen for binary mesh URLs from the backend.
  EventsOn('input-mesh', (event: { gen: number; url: string; previewScale?: number; extentMM?: number; bboxMin?: [number, number, number]; bboxMax?: [number, number, number] }) => {
    if (event.gen !== run.id) return;
    run.input.meshUrl = event.url;
    // The overlay is set/cleared deterministically by the
    // 'input-overlay-mesh' event the pipeline fires after this one, so
    // we don't need to clear it here.
    if (event.extentMM !== undefined && (nativeExtentMM === null || !approxEqual(nativeExtentMM, event.extentMM))) {
      nativeExtentMM = event.extentMM;
    }
    if (event.previewScale !== undefined) {
      previewScale = event.previewScale;
    }
    // Update the model bbox for the Split offset slider. The bbox
    // is in original-mesh coords (mm, post-scale, post-normalizeZ).
    if (event.bboxMin && event.bboxMax) {
      const newMin = event.bboxMin;
      const newMax = event.bboxMax;
      modelBBoxMin = newMin;
      modelBBoxMax = newMax;
      // If Split was enabled with the previous model's bbox in mind
      // (offset now outside the new model's range), recentre the offset
      // on the new model's bbox along the chosen axis. splitOffset is a
      // fraction of the print extent, so compare in mm via that extent.
      const ext = scaledMaxExtentMM;
      if (ext && ext > 0) {
        const lo = newMin[splitAxis];
        const hi = newMax[splitAxis];
        const offMM = splitOffset * ext;
        if (offMM < lo || offMM > hi) {
          splitOffset = ((lo + hi) / 2) / ext;
        }
      }
    }
  });
  EventsOn('input-overlay-mesh', (event: { gen: number; url: string }) => {
    if (event.gen !== run.id) return;
    // Empty url means the pipeline explicitly told us there's no overlay
    // (e.g. alpha-wrap turned off). Clear the previous overlay if any.
    run.input.overlayUrl = event.url || undefined;
  });
  EventsOn('wrapped-mesh', (event: { gen: number; url: string }) => {
    if (event.gen !== run.id) return;
    // Empty url = alpha-wrap is off; drop the wrapped preview and
    // force the toggle back to the input view.
    run.input.wrappedUrl = event.url || undefined;
    if (!run.input.wrappedUrl && inputViewMode === 'wrapped') {
      inputViewMode = 'input';
    }
  });
  EventsOn('output-preview-mesh', (event: { gen: number; url: string }) => {
    if (event.gen !== run.id) return;
    // Grey in-progress geometry. The viewer prefers the final mesh, so
    // this only shows until output-mesh arrives for the same run.
    run.output.previewUrl = event.url;
  });
  EventsOn('output-mesh', (event: { gen: number; url: string }) => {
    if (event.gen !== run.id) return;
    run.output.finalUrl = event.url;
    // Final coloured mesh is in; drop the grey preview reference.
    run.output.previewUrl = undefined;
  });

  // Listen for pipeline result events from the backend worker.
  EventsOn('pipeline-done', (event: { gen: number; duration: number; colorUsage?: ColorUsage[] }) => {
    if (event.gen !== run.id) return;
    run.phase = 'done';
    run.output.colorUsage = event.colorUsage ?? [];
    // The action bar's "✓ Up to date" state conveys success now, so clear the
    // transient status line — but preserve any warning emitted during the run.
    if (statusType !== 'warning') {
      statusMessage = '';
      statusType = 'idle';
    }
  });
  EventsOn('pipeline-error', (event: { gen: number; message: string }) => {
    if (event.gen !== run.id) return;
    run.phase = 'error';
    run.error = event.message;
    inputError = event.message;
    statusMessage = `Error: ${event.message}`;
    statusType = 'error';
  });
  EventsOn('pipeline-warning', (event: { gen: number; kind: string; message: string }) => {
    if (event.gen !== run.id) return;
    // Every warning updates the status bar (chronological event log).
    // Kind-tagged warnings ALSO pin to their inline home (persistent
    // state-of-the-input) — the two surfaces answer different
    // questions and aren't mutually exclusive.
    if (statusType !== 'error') {
      statusMessage = event.message;
      statusType = 'warning';
    }
    // Route kind-tagged warnings to their inline UI homes. The kind
    // string is the structured contract between the Go pipeline
    // (progress.WarnKind* constants) and this listener — no
    // substring-matching the message body, which is fragile to
    // rewording. File-pick validation only runs at pick time, so a
    // failure that surfaces only at run time (e.g. an asset that's
    // missing on this machine but was present when the settings file
    // was saved) reaches the right banner via the kind tag.
    if (event.kind === 'materialx-base-color') {
      baseMaterialXError = event.message;
    }
  });
  EventsOn('pipeline-needs-force', (event: { gen: number; extentMM: number }) => {
    if (event.gen !== run.id) return;
    // Not running, not an error — the run paused awaiting confirmation.
    // 'idle' hides the progress overlay; the force dialog drives the next
    // step (Continue → runPipeline(true), which starts a fresh run).
    run.phase = 'idle';
    forceExtentMM = event.extentMM;
    forceDialogOpen = true;
    statusMessage = '';
    statusType = 'idle';
  });
  EventsOn('palette-resolved', (event: { gen: number; colors: { hex: string; label: string; td: number }[] }) => {
    if (event.gen !== run.id) return;
    // The palette is [locked..., auto...]. Extract the auto portion.
    const numLocked = colorSlots.filter(s => s !== null).length;
    const collName = inventoryCollection;
    resolvedUnlockedColors = event.colors.slice(numLocked).map(c => ({ ...c, collection: collName }));
  });
  EventsOn('pipeline-stage', (event: { gen: number; stage: string; status: string; hasProgress: boolean; total: number }) => {
    if (event.gen !== run.id) return;
    const now = Date.now();
    if (event.status === 'running') {
      const existing = run.stages.find(s => s.name === event.stage);
      if (existing) {
        existing.status = 'running';
        existing.hasProgress = event.hasProgress;
        existing.total = event.total;
        existing.current = 0;
        existing.cached = false;
        existing.startedAt = now;
        existing.elapsed = 0;
        existing.lastBeatAt = now;
      } else {
        run.stages.push({
          name: event.stage,
          status: 'running',
          hasProgress: event.hasProgress,
          current: 0,
          total: event.total,
          cached: false,
          startedAt: now,
          elapsed: 0,
          lastBeatAt: now,
        });
      }
      startStageTimer();
    } else if (event.status === 'cached') {
      // Cache-replay marker: the stage started (a 'running' event
      // preceded this) and its blob is being decoded from disk. Label
      // the row "(cache)"; it persists through 'done'.
      const existing = run.stages.find(s => s.name === event.stage);
      if (existing) {
        existing.cached = true;
        existing.lastBeatAt = now;
      } else {
        // Shouldn't happen — StageStart precedes the cache marker — but
        // stay defensive so a lone marker still shows a row.
        run.stages.push({
          name: event.stage,
          status: 'running',
          hasProgress: false,
          current: 0,
          total: 0,
          cached: true,
          startedAt: now,
          elapsed: 0,
          lastBeatAt: now,
        });
      }
    } else if (event.status === 'done') {
      const existing = run.stages.find(s => s.name === event.stage);
      if (existing) {
        existing.status = 'done';
        existing.elapsed = (now - existing.startedAt) / 1000;
      } else {
        run.stages.push({
          name: event.stage,
          status: 'done',
          hasProgress: false,
          current: 0,
          total: 0,
          cached: false,
          startedAt: now,
          elapsed: 0,
          lastBeatAt: now,
        });
      }
    }
  });
  EventsOn('pipeline-progress', (event: { gen: number; stage: string; current: number }) => {
    if (event.gen !== run.id) return;
    const existing = run.stages.find(s => s.name === event.stage);
    if (existing) {
      existing.current = event.current;
      existing.lastBeatAt = Date.now();
    }
  });
  // Backend liveness ticks for running stages that emit no natural
  // progress (see progress.Monitor on the Go side). Only refreshes
  // lastBeatAt — the stalled indicator derives from its absence.
  EventsOn('pipeline-heartbeat', (event: { gen: number; stage: string; elapsedMs: number }) => {
    if (event.gen !== run.id) return;
    const existing = run.stages.find(s => s.name === event.stage);
    if (existing && existing.status === 'running') {
      existing.lastBeatAt = Date.now();
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

  // Auto-trigger the pipeline whenever any input that affects the
  // backend changes. We derive the dependency set from settingsForBackend()
  // itself: Svelte 5 registers $state reads inside the effect, and that
  // builder reads exactly the values that flow to the backend (committed
  // slider values, so dragging doesn't reprocess until release). Adding a
  // new pipeline-relevant setting requires wiring it into buildSettings
  // anyway, so this effect tracks it automatically. JSON.stringify produces
  // a stable deep read for arrays/objects so element mutations don't slip
  // past granular reactivity.
  //
  // inventoryCollectionColors and reloadSeq are tracked explicitly: the
  // backend re-resolves the inventory collection by name, so editing the
  // active collection's colors changes settingsForBackend()'s output only
  // through this resolved list; reloadSeq is bumped to force a re-read of
  // the same input file and is not part of the persisted settings.
  let initialized = false;
  $effect(() => {
    void JSON.stringify(settingsForBackend());
    void JSON.stringify(inventoryCollectionColors);
    void reloadSeq;
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
      // Fraction of the model's max extent (was 20 mm; 0.2 ≈ 20 mm on a
      // 100 mm model). Shown back in mm by the Scale slider.
      scale: 0.2,
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
    // Store the placement as a fraction of the model's max extent — a
    // scale-invariant coordinate that tracks the print size automatically.
    // The input viewer renders at native mm (= the model at scale 1), so a
    // clicked point divided by the native extent IS that fraction, with no
    // dependence on the current Size/Scale knobs.
    const ext = nativeExtentMM;
    if (ext === null || ext <= 0) return; // no known extent yet — can't place
    const frac: [number, number, number] = [
      point[0] / ext,
      point[1] / ext,
      point[2] / ext,
    ];
    stickers[placingStickerIndex] = {
      ...stickers[placingStickerIndex],
      center: frac,
      normal,
      up: cameraUp,
    };
    stickers = stickers;
    placingStickerIndex = -1;
  }

  // previewScale converts pipeline-mm (the final, scaled print size) to the
  // input viewer's render frame, which the backend keeps at a constant native
  // size regardless of the Size/Scale knobs (= unitScale/totalScale, emitted
  // on each input-mesh). Used by the split-plane overlay (pipeline-mm bbox →
  // render frame) and the sticker placement cursor.
  let previewScale = $state(1);
  // Native max extent of the loaded model in mm (scale=1.0, size=unset),
  // reported by the backend on each input-mesh. Feeds scaledMaxExtentMM in
  // scale mode, the fraction conversion at sticker placement, and the
  // placement cursor's preview-frame size.
  let nativeExtentMM: number | null = $state(null);

  // scaledMaxExtentMM is the model's max bounding-box dimension at its final
  // PRINT size, in mm — the denominator the size-relative settings (split
  // offset, sticker center/scale, MaterialX tile size) are stored as a
  // fraction of. In size mode that's exactly the target Size; in scale mode
  // it's the backend-reported native extent times the scale factor. null
  // until enough is known (scale mode needs the native extent from the first
  // input-mesh). Mirrors the Go pipeline's modelMaxExtent of the scaled model.
  const scaledMaxExtentMM = $derived.by((): number | null => {
    if (sizeMode === 'size') {
      const s = parseFloat(sizeValue);
      return isFinite(s) && s > 0 ? s : null;
    }
    const k = parseFloat(scaleValue);
    if (nativeExtentMM === null || nativeExtentMM <= 0) return null;
    return isFinite(k) && k > 0 ? nativeExtentMM * k : null;
  });

  // Collapsed-header state summaries. Each is a muted one-liner shown to the
  // right of the section title (open or closed), derived purely from the
  // controls' bound state — presentation only, never feeds the pipeline.
  const modelSummary = $derived.by(() => {
    const parts: string[] = [];
    const name = inputFile ? inputFile.split(/[/\\]/).pop() : '';
    if (name) parts.push(name);
    if (scaledMaxExtentMM && scaledMaxExtentMM > 0) parts.push(`${Math.round(scaledMaxExtentMM)} mm`);
    if (meshRepair !== 'none') parts.push('repaired');
    return parts.length ? parts.join(' · ') : '—';
  });
  const appearanceSummary = $derived.by(() => {
    const n = colorSlots.length; // total palette slots, auto-filled included
    const label = DITHER_OPTIONS.find(o => o.value === dither)?.label ?? dither;
    return `${n} ${n === 1 ? 'color' : 'colors'} · ${label}`;
  });

  // ---- Run feedback: per-color usage (step 6 Part B) ----------------------
  // colorUsage arrives on pipeline-done, ordered by palette index. The
  // pipeline builds the palette as [locked slots..., auto slots...], the same
  // order the palette-resolved handler assumes — so map each slot to its
  // palette index the same way to line usage up with the slot rows.
  const colorUsage = $derived(run.output.colorUsage ?? []);
  const totalUsageTriangles = $derived(colorUsage.reduce((s, u) => s + u.triangles, 0));
  const slotUsage = $derived.by((): (ColorUsage | null)[] => {
    if (colorUsage.length === 0) return colorSlots.map(() => null);
    const numLocked = colorSlots.filter(s => s !== null).length;
    let lockedIdx = 0;
    let autoIdx = 0;
    return colorSlots.map(slot => {
      const pi = slot !== null ? lockedIdx++ : numLocked + autoIdx++;
      return colorUsage[pi] ?? null;
    });
  });
  // Locked colors that got zero triangles in the last run — the user pinned
  // them but nothing used them. Auto slots are excluded (the picker chose
  // them, so an unused auto color is not a user mistake).
  const unusedLockedCount = $derived(
    colorSlots.reduce((n, slot, i) =>
      n + (slot !== null && slotUsage[i] !== null && slotUsage[i]!.triangles === 0 ? 1 : 0), 0)
  );

  // ---- Persistent action-bar run state (step 6 Part B) --------------------
  // Derived from the run lifecycle record, so it inherits the exact-match
  // staleness gating (only the current run's events mutate `run`). Replaces
  // the bare statusMessage line; statusMessage now carries only transient
  // save/export/warning detail shown beneath the state.
  const runStateLabel = $derived(
    run.phase === 'running' ? 'Recomputing…'
    : run.phase === 'error' ? (run.error || 'Error')
    : run.phase === 'done'  ? '✓ Up to date'
    : ''
  );
  const runStateClass = $derived(
    run.phase === 'error' ? 'text-red-500'
    : run.phase === 'done' ? 'text-green-500'
    : 'text-muted-foreground'
  );
  // Errors already show in the main label; keep other transient messages
  // (Saved…, Exported…, warnings, "Please select an input file") as a detail
  // line so nothing is lost. The color class is computed inline in the
  // template — reading statusType there avoids TS narrowing it to its
  // initializer literal, which happens in a script-level $derived.
  const actionDetail = $derived(run.phase === 'error' ? '' : statusMessage);
  const modifySummary = $derived.by(() => {
    const parts: string[] = [];
    const n = stickers.length;
    if (n > 0) parts.push(`${n} ${n === 1 ? 'sticker' : 'stickers'}`);
    if (splitEnabled) {
      const axis = SPLIT_AXIS_OPTIONS.find(o => o.value === splitAxis)?.label ?? '';
      parts.push(`split ${axis}`);
    }
    return parts.length ? parts.join(' · ') : '—';
  });
  const printSummary = $derived.by(() => {
    const name = currentPrinter?.displayName ?? printerId;
    return `${name} · ${nozzleDiameter} · ${layerHeight} mm`;
  });

  // MaterialX tile size is stored as a fraction of the print extent; the
  // "Tile size" input shows/edits it in mm.
  const baseMaterialXTileDisplayMM = $derived(
    scaledMaxExtentMM && scaledMaxExtentMM > 0
      ? baseMaterialXTileMM * scaledMaxExtentMM
      : baseMaterialXTileMM
  );
  function setBaseMaterialXTileMM(mm: number) {
    const ext = scaledMaxExtentMM;
    if (ext && ext > 0 && isFinite(mm)) baseMaterialXTileMM = mm / ext;
  }

  // Legacy-file migration: a settings file written before 0.9.6 stores the
  // size-relative fields as absolute mm. applySettings loads those values
  // as-is and sets this flag; once the print extent is known we divide them
  // into fractions (the canonical in-memory form) exactly once. Newer files
  // already hold fractions, so the flag stays false and nothing runs.
  let pendingLegacyUnitConversion = $state(false);
  $effect(() => {
    if (!pendingLegacyUnitConversion) return;
    const ext = scaledMaxExtentMM;
    if (ext === null || ext <= 0) return;
    // untrack the writes so re-reading the same state we set here can't
    // retrigger this effect (the only tracked deps stay scaledMaxExtentMM
    // and the flag, which flips false below to end the conversion).
    untrack(() => {
      splitOffset = splitOffset / ext;
      baseMaterialXTileMM = baseMaterialXTileMM / ext;
      stickers = stickers.map(s => ({
        ...s,
        center: s.center
          ? [s.center[0] / ext, s.center[1] / ext, s.center[2] / ext] as [number, number, number]
          : s.center,
        scale: s.scale / ext,
      }));
      pendingLegacyUnitConversion = false;
    });
  });

  // Tolerance for treating two extents as equal. The backend reports float32
  // values, so re-deriving them on the JS side can differ by a few ulps;
  // without a tolerance those micro-differences would churn reactive state.
  const SCALE_EPS = 1e-5;
  function approxEqual(a: number, b: number) {
    const d = Math.abs(a - b);
    const m = Math.max(Math.abs(a), Math.abs(b));
    return d <= SCALE_EPS * (m > 0 ? m : 1);
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

  // Abandons the current viewer state. Used when the current model is
  // being replaced (File > Open a model directly, File > New, loading a
  // settings JSON for a different model) — the prior mesh would otherwise
  // render until the pipeline regenerates everything. Kept as a helper so
  // applySettings and proceedWithInput stay in lockstep.
  //
  // Bumping runId here is the crux of the fix: replacing the model cancels
  // nothing on the backend until the debounced runPipeline fires ~300ms
  // later, so the in-flight previous run keeps emitting events. Allocating
  // a fresh id orphans those events (their gen no longer matches run.id),
  // which is precisely what stops a superseded run's output from being
  // written into the viewer while the next model loads.
  function clearViewportMesh() {
    run = idleRun(++runId);
    inputViewMode = 'input';
    modelBBoxMin = null;
    modelBBoxMax = null;
  }

  function proceedWithInput(path: string) {
    clearViewportMesh();
    inputFile = path;
    // The input model is just another setting now: changing it must not reset
    // the rest of the configuration. Stickers and warp pins are preserved —
    // sticker positions/scales are stored as fractions of the model's max
    // extent, so they rescale with the new model rather than being pinned to
    // stale mm coordinates; warp pins are color-based and carry over too.
    // Clear only the cached native extent (so mm displays recompute against the
    // new model) and cancel any in-progress placement that referenced the old
    // model's geometry.
    placingStickerIndex = -1;
    pickingPinIndex = -1;
    nativeExtentMM = null;
    reloadSeq++;
    // Clear settingsPath synchronously so a save before DefaultSettingsPath
    // resolves (or if it fails) can't write to the previous model's file.
    settingsPath = '';
    DefaultSettingsPath(path).then(p => settingsPath = p).catch(() => {});
    addRecentModel(path);
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

  // Clear baseMaterialXError on any path change so the previous file's
  // error doesn't briefly linger while validateMaterialXFile is still
  // in flight on the new pick. validateMaterialXFile and the
  // pipeline-warning handler are the only setters; both run after
  // this clear, so the error stays in sync.
  let prevMtlxPath = '';
  $effect(() => {
    if (baseMaterialXPath !== prevMtlxPath) {
      prevMtlxPath = baseMaterialXPath;
      baseMaterialXError = '';
    }
  });

  // Builds the plain settings object that mirrors Go's settings.Settings.
  // sliderSource picks whether the debounced slider fields read their live
  // ('raw') or committed values:
  //   • 'raw'       — for SAVE and the factory-default snapshot: persist
  //                   exactly what the user currently sees.
  //   • 'committed' — for the backend payload and the auto-rerun $effect:
  //                   dragging a slider must not reprocess until release.
  // Only the chosen branch's $state is read, so the $effect that stringifies
  // the 'committed' build never tracks (and re-fires on) the raw slider drag.
  function buildSettings(sliderSource: 'raw' | 'committed') {
    const useC = sliderSource === 'committed';
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
      baseColorMode,
      baseMaterialXPath,
      baseMaterialXTileMM,
      baseMaterialXTriplanarSharpness,
      colorSlots: colorSlots.map(s => s ? { hex: s.hex, label: s.label, collection: s.collection, td: s.td } : null),
      inventoryCollection,
      brightness: useC ? committedBrightness : brightness,
      contrast: useC ? committedContrast : contrast,
      saturation: useC ? committedSaturation : saturation,
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
      riemersmaBias: useC ? committedRiemersmaBias : riemersmaBias,
      blueNoiseTol: useC ? committedBlueNoiseTol : blueNoiseTol,
      colorSnap: useC ? committedColorSnap : colorSnap,
      noMerge,
      noCellMerge,
      noSimplify,
      honorTD,
      tdModel,
      infillColor,
      colorAwareCells,
      colorRegionContrast: useC ? committedColorRegionContrast : colorRegionContrast,
      regionDither,
      regionDitherDeltaE: useC ? committedRegionDitherDeltaE : regionDitherDeltaE,
      rejectColorOutliers,
      stats,
      showSampledColors,
      meshRepair,
      alphaWrapAlpha: String(alphaWrapAlpha),
      alphaWrapOffset: String(alphaWrapOffset),
      fwnDetailXY: String(fwnDetailXY),
      fwnDetailZ: String(fwnDetailZ),
      layer0AdhesionXYScale: useC ? committedLayer0AdhesionXYScale : layer0AdhesionXYScale,
      upperLayerXYScale: useC ? committedUpperLayerXYScale : upperLayerXYScale,
      splitEnabled,
      splitAxis,
      splitOffset,
      splitTiltA,
      splitTiltB,
      splitConnectorStyle,
      splitConnectorCount,
      splitConnectorDiamMM,
      splitConnectorDepthMM,
      splitClearanceMM,
      splitOrientationA,
      splitOrientationB,
    };
  }

  // Raw snapshot for SAVE / factory defaults / the key-guard below.
  function serializeSettings() {
    return buildSettings('raw');
  }

  // Committed snapshot sent to the backend; Go's settings.ToOptions turns
  // it into pipeline.Options (the same path the CLI uses). Replaces the old
  // JS buildOpts() so the GUI and CLI can never diverge.
  function settingsForBackend(): settings.Settings {
    return buildSettings('committed') as unknown as settings.Settings;
  }

  // Compile-time guard: serializeSettings() and Go's Settings struct
  // (reflected via the Wails-generated main.Settings TS class) must
  // have IDENTICAL key sets. Two failure modes are covered:
  //
  //   • Frontend → Go: a key in serializeSettings missing from the
  //     Go struct is silently dropped by the Wails marshaller and
  //     disappears on the next load.
  //
  //   • Go → frontend: a Go field that serializeSettings forgets to
  //     emit is never saved at all — looks like the user can change
  //     the setting but its value never reaches disk. This is the
  //     direction that originally hid riemersmaBias / blueNoiseTol /
  //     layer0AdhesionXYScale / upperLayerXYScale and is what the
  //     symmetric `extends` below catches.
  //
  // We compare keys only (not value types) to sidestep Wails
  // generator quirks around []*T (drops null) and [N]T (widens to
  // T[]). When this guard fires it resolves the type to `never`,
  // and `const _: never = true` fails compilation.
  // 'alphaWrap' is excluded: it is a legacy-load-only Go field (json
  // omitempty) that the frontend intentionally no longer serializes —
  // settings.Load migrates it into meshRepair and Save drops it. It has
  // no matching serializeSettings key by design, so exclude it here.
  type _GoSettingsKeys = keyof Omit<settings.Settings, 'convertValues' | 'alphaWrap'>;
  type _FrontendSettingsKeys = keyof ReturnType<typeof serializeSettings>;
  type _SettingsKeysMatchGo =
    _FrontendSettingsKeys extends _GoSettingsKeys
      ? _GoSettingsKeys extends _FrontendSettingsKeys
        ? true
        : never
      : never;
  const _settingsKeysMatchGo: _SettingsKeysMatchGo = true;
  void _settingsKeysMatchGo;

  // Validation helpers for applySettings. Every field should fall back
  // to its FACTORY_DEFAULTS value when missing from the JSON or set to
  // an unsupported value, so partial / older / corrupted settings
  // files don't leave the UI in an unreachable state.
  function pickString(v: unknown, def: string): string {
    return typeof v === 'string' ? v : def;
  }
  function pickNumber(v: unknown, def: number): number {
    return typeof v === 'number' && Number.isFinite(v) ? v : def;
  }
  function pickBool(v: unknown, def: boolean): boolean {
    return typeof v === 'boolean' ? v : def;
  }
  function pickEnum<T extends string>(v: unknown, allowed: readonly T[], def: T): T {
    return typeof v === 'string' && (allowed as readonly string[]).includes(v) ? (v as T) : def;
  }
  function pickIntEnum<T extends number>(v: unknown, allowed: readonly T[], def: T): T {
    return typeof v === 'number' && (allowed as readonly number[]).includes(v) ? (v as T) : def;
  }

  // Validates a MaterialX file (existence + parse + base-color sampler
  // compile) and surfaces any problem at file-pick time — both as a
  // status-bar warning and as a persistent inline banner adjacent to
  // the file display (baseMaterialXError). The inline banner is the
  // load-bearing one: the user-reported failure mode that motivated it
  // was "I selected a .zip, the UI shows the file is loaded, the
  // pipeline produced output, I didn't notice the texture wasn't
  // applied" — i.e. the status-bar warning got missed.
  //
  // Validation success clears baseMaterialXError so a fresh pick can
  // recover from an earlier broken pick without an extra interaction.
  //
  // The result race-guards against concurrent picks: if the user
  // selects file B while validation of file A is in flight, A's late
  // result is dropped instead of overwriting whatever we already said
  // about B.
  async function validateMaterialXFile(path: string): Promise<void> {
    if (!path) return;
    const warning = await ValidateMaterialX(path);
    if (path !== baseMaterialXPath) return;
    baseMaterialXError = warning;
    if (warning) {
      statusMessage = warning;
      statusType = 'warning';
    }
  }

  // Compound-value validators used by SETTINGS_SCHEMA below. Each takes
  // the raw JSON value plus the FACTORY_DEFAULTS value for that field
  // and returns a typed, sanitised result; if the raw value is missing
  // or shaped wrong, the default is returned (deep-cloned for arrays
  // and objects so the in-memory $state never aliases FACTORY_DEFAULTS).
  function vColor(raw: unknown, def: any): ColorInfo | null {
    if (raw && typeof raw === 'object') {
      const o = raw as any;
      return { hex: pickString(o.hex, ''), label: pickString(o.label, ''), collection: pickString(o.collection, '') };
    }
    return def ? structuredClone(def) : null;
  }
  function vColorSlots(raw: unknown, def: any): ColorSlot[] {
    if (Array.isArray(raw)) {
      return raw.map((c: any) => c && typeof c === 'object'
        ? { hex: pickString(c.hex, ''), label: pickString(c.label, ''), collection: pickString(c.collection, ''), td: pickNumber(c.td, 1) }
        : null);
    }
    return structuredClone(def);
  }
  function vWarpPins(raw: unknown, def: any): WarpPinUI[] {
    if (Array.isArray(raw)) {
      return raw.map((p: any) => ({
        sourceHex: pickString(p?.sourceHex, ''),
        targetHex: pickString(p?.targetHex, ''),
        targetLabel: pickString(p?.targetLabel, ''),
        sigma: pickNumber(p?.sigma, 0),
      }));
    }
    return structuredClone(def);
  }
  function vStickers(raw: unknown, def: any): StickerUI[] {
    if (Array.isArray(raw)) {
      return raw.map((st: any) => {
        const imagePath = pickString(st?.imagePath, '');
        return {
          imagePath,
          fileName: imagePath.split(/[/\\]/).pop() || imagePath,
          thumbnail: '',
          center: st?.center,
          normal: st?.normal,
          up: st?.up,
          scale: pickNumber(st?.scale, 1),
          rotation: pickNumber(st?.rotation, 0),
          maxAngle: pickNumber(st?.maxAngle, 0),
          mode: pickEnum(st?.mode, STICKER_MODE_VALUES, 'unfold'),
        };
      });
    }
    return structuredClone(def);
  }

  // Single source of truth for loading a settings file: one entry per
  // persisted field, declaring how to validate the raw JSON value and
  // where to store it. applySettings() iterates this list, so:
  //   * Missing-→-default is automatic — applySettings doesn't know
  //     about individual fields, and any field absent from rawIn falls
  //     back to its FACTORY_DEFAULTS value.
  //   * Adding a new persisted setting only takes three steps: declare
  //     the $state, add the field to serializeSettings (so it appears
  //     in FACTORY_DEFAULTS), and add one entry here.
  // The exhaustiveness check after FACTORY_DEFAULTS is captured will
  // warn if serializeSettings ever has a key that's missing here.
  type SettingSpec = {
    key: string;
    validate: (raw: unknown, def: any) => any;
    apply: (v: any) => void;
  };
  const SETTINGS_SCHEMA: SettingSpec[] = [
    { key: 'inputFile',                       validate: pickString,                                        apply: (v) => { inputFile = v; } },
    { key: 'objectIndex',                     validate: pickNumber,                                        apply: (v) => { objectIndex = v; } },
    { key: 'sizeMode',                        validate: (v, d) => pickEnum(v, SIZE_MODE_VALUES, d),        apply: (v) => { sizeMode = v; } },
    { key: 'sizeValue',                       validate: pickString,                                        apply: (v) => { sizeValue = v; } },
    { key: 'scaleValue',                      validate: pickString,                                        apply: (v) => { scaleValue = v; } },
    { key: 'printer',                         validate: pickString,                                        apply: (v) => { printerId = v; } },
    { key: 'nozzleDiameter',                  validate: pickString,                                        apply: (v) => { nozzleDiameter = v; } },
    { key: 'layerHeight',                     validate: pickString,                                        apply: (v) => { layerHeight = v; } },
    { key: 'baseColor',                       validate: vColor,                                            apply: (v) => { baseColor = v; } },
    { key: 'baseMaterialXPath',               validate: pickString,                                        apply: (v) => { baseMaterialXPath = v; } },
    { key: 'baseMaterialXTileMM',             validate: pickNumber,                                        apply: (v) => { baseMaterialXTileMM = v; } },
    { key: 'baseMaterialXTriplanarSharpness', validate: pickNumber,                                        apply: (v) => { baseMaterialXTriplanarSharpness = v; } },
    { key: 'baseColorMode',                   validate: (v, d) => pickEnum(v, BASE_COLOR_MODE_VALUES, d),  apply: (v) => { baseColorMode = v; } },
    { key: 'colorSlots',                      validate: vColorSlots,                                       apply: (v) => { colorSlots = v; } },
    { key: 'inventoryCollection',             validate: pickString,                                        apply: (v) => { inventoryCollection = v; } },
    { key: 'brightness',                      validate: pickNumber,                                        apply: (v) => { brightness = v; committedBrightness = v; } },
    { key: 'contrast',                        validate: pickNumber,                                        apply: (v) => { contrast = v; committedContrast = v; } },
    { key: 'saturation',                      validate: pickNumber,                                        apply: (v) => { saturation = v; committedSaturation = v; } },
    { key: 'warpPins',                        validate: vWarpPins,                                         apply: (v) => { warpPins = v; } },
    { key: 'stickers',                        validate: vStickers,                                         apply: (v) => { stickers = v; } },
    { key: 'dither',                          validate: (v, d) => pickEnum(v, DITHER_VALUES, d),           apply: (v) => { dither = v; } },
    { key: 'riemersmaBias',                   validate: pickNumber,                                        apply: (v) => { riemersmaBias = v; committedRiemersmaBias = v; } },
    { key: 'blueNoiseTol',                    validate: pickNumber,                                        apply: (v) => { blueNoiseTol = v; committedBlueNoiseTol = v; } },
    { key: 'colorSnap',                       validate: pickNumber,                                        apply: (v) => { colorSnap = v; committedColorSnap = v; } },
    { key: 'noMerge',                         validate: pickBool,                                          apply: (v) => { noMerge = v; } },
    { key: 'noCellMerge',                       validate: pickBool,                                          apply: (v) => { noCellMerge = v; } },
    { key: 'noSimplify',                      validate: pickBool,                                          apply: (v) => { noSimplify = v; } },
    { key: 'honorTD',                         validate: pickBool,                                          apply: (v) => { honorTD = v; } },
    { key: 'tdModel',                         validate: pickString,                                        apply: (v) => { tdModel = v; } },
    { key: 'infillColor',                     validate: pickString,                                        apply: (v) => { infillColor = v; } },
    { key: 'colorAwareCells',                 validate: pickBool,                                          apply: (v) => { colorAwareCells = v; } },
    { key: 'colorRegionContrast',             validate: pickNumber,                                        apply: (v) => { colorRegionContrast = v; committedColorRegionContrast = v; } },
    { key: 'regionDither',                    validate: pickBool,                                          apply: (v) => { regionDither = v; } },
    { key: 'regionDitherDeltaE',              validate: pickNumber,                                        apply: (v) => { regionDitherDeltaE = v; committedRegionDitherDeltaE = v; } },
    { key: 'rejectColorOutliers',             validate: pickBool,                                          apply: (v) => { rejectColorOutliers = v; } },
    { key: 'stats',                           validate: pickBool,                                          apply: (v) => { stats = v; } },
    { key: 'showSampledColors',               validate: pickBool,                                          apply: (v) => { showSampledColors = v; } },
    { key: 'meshRepair',                      validate: (v, d) => pickEnum(v, MESH_REPAIR_VALUES, d),      apply: (v) => { meshRepair = v; } },
    { key: 'alphaWrapAlpha',                  validate: pickString,                                        apply: (v) => { alphaWrapAlpha = v; } },
    { key: 'alphaWrapOffset',                 validate: pickString,                                        apply: (v) => { alphaWrapOffset = v; } },
    { key: 'fwnDetailXY',                     validate: pickString,                                        apply: (v) => { fwnDetailXY = v; } },
    { key: 'fwnDetailZ',                      validate: pickString,                                        apply: (v) => { fwnDetailZ = v; } },
    { key: 'layer0AdhesionXYScale',           validate: pickNumber,                                        apply: (v) => { layer0AdhesionXYScale = v; committedLayer0AdhesionXYScale = v; } },
    { key: 'upperLayerXYScale',               validate: pickNumber,                                        apply: (v) => { upperLayerXYScale = v; committedUpperLayerXYScale = v; } },
    { key: 'splitEnabled',                    validate: pickBool,                                          apply: (v) => { splitEnabled = v; } },
    { key: 'splitAxis',                       validate: (v, d) => pickIntEnum(v, SPLIT_AXIS_VALUES, d),    apply: (v) => { splitAxis = v; } },
    { key: 'splitOffset',                     validate: pickNumber,                                        apply: (v) => { splitOffset = v; } },
    { key: 'splitTiltA',                      validate: pickNumber,                                        apply: (v) => { splitTiltA = v; } },
    { key: 'splitTiltB',                      validate: pickNumber,                                        apply: (v) => { splitTiltB = v; } },
    { key: 'splitConnectorStyle',             validate: (v, d) => pickEnum(v, SPLIT_CONNECTOR_VALUES, d),  apply: (v) => { splitConnectorStyle = v; } },
    { key: 'splitConnectorCount',             validate: pickNumber,                                        apply: (v) => { splitConnectorCount = v; } },
    { key: 'splitConnectorDiamMM',            validate: pickNumber,                                        apply: (v) => { splitConnectorDiamMM = v; } },
    { key: 'splitConnectorDepthMM',           validate: pickNumber,                                        apply: (v) => { splitConnectorDepthMM = v; } },
    { key: 'splitClearanceMM',                validate: pickNumber,                                        apply: (v) => { splitClearanceMM = v; } },
    { key: 'splitOrientationA',               validate: (v, d) => pickEnum(v, SPLIT_ORIENTATION_VALUES, d), apply: (v) => { splitOrientationA = v; } },
    { key: 'splitOrientationB',               validate: (v, d) => pickEnum(v, SPLIT_ORIENTATION_VALUES, d), apply: (v) => { splitOrientationB = v; } },
  ];

  function applySettings(rawIn: any, legacyAbsoluteUnits = false) {
    const D: any = FACTORY_DEFAULTS;
    const s: any = rawIn ?? {};

    // Clear the cached extent: if this settings file points at a different
    // input model, the old extent would mis-scale the displayed mm values
    // until the backend replies with the true extent.
    nativeExtentMM = null;

    // Snapshot inputFile before the schema overwrites it, so we can
    // detect when the load points at a different model and clear the
    // now-stale viewport mesh URLs below. (Local name avoids shadowing
    // the module-level prevInputFile used by the camera-reset effect.)
    const priorInputFile = inputFile;

    // Drive every persisted field off SETTINGS_SCHEMA. Anything missing
    // from `s` automatically falls back to the FACTORY_DEFAULTS value
    // captured at script init, so loading a partial settings file
    // never leaves a previous load's value behind.
    for (const spec of SETTINGS_SCHEMA) {
      spec.apply(spec.validate(s[spec.key], D[spec.key]));
    }

    // Pre-0.9.6 files store the size-relative fields (split offset, sticker
    // center/scale, MaterialX tile size) as absolute mm; newer files store
    // fractions of the print extent. When legacy, defer a one-shot mm→fraction
    // conversion until the extent is known (see the effect above). Always
    // assigned so a fraction file clears any flag left by a prior legacy load.
    pendingLegacyUnitConversion = legacyAbsoluteUnits;

    // If the inputFile changed (different model, or reset to empty via
    // File > New), the previous model's cached mesh URLs and bbox no
    // longer match — clear them so the user doesn't see the old
    // textured mesh briefly while the auto-pipeline regenerates them.
    if (inputFile !== priorInputFile) {
      clearViewportMesh();
    }

    // Legacy: settings files saved before nozzleDiameter was renamed
    // used "nozzle". Honour the alias only when the new key is absent
    // (otherwise the schema entry above already handled it).
    if (s.nozzleDiameter === undefined && typeof s.nozzle === 'string') {
      nozzleDiameter = s.nozzle;
    }

    // Legacy: settings files predating baseColorMode lack the explicit
    // mode field. If the path is set but no mode was specified, default
    // to "texture" so the load matches what the user saved.
    if (s.baseColorMode === undefined && baseMaterialXPath) {
      baseColorMode = 'texture';
    }

    reconcilePrinterSelection();
    loadInventoryCollectionColors(inventoryCollection);

    // Best-effort check: when a settings file is loaded on a different
    // machine than where it was saved, the .mtlx path may not resolve,
    // or the file may use unsupported nodes. Surface a warning
    // immediately so the user knows before they first click Generate.
    if (baseMaterialXPath) {
      validateMaterialXFile(baseMaterialXPath);
    }

    // Sticker thumbnails are loaded from disk asynchronously.
    stickers.forEach((st, i) => {
      ReadStickerThumbnail(st.imagePath).then(thumb => {
        stickers[i] = { ...stickers[i], thumbnail: thumb };
        stickers = stickers;
      }).catch(() => {});
    });
  }

  // A just-loaded legacy file still holds raw-mm values until the deferred
  // mm→fraction conversion runs (once the model extent is known). Saving in
  // that window would persist mm as fractions, corrupting the file. Refuse.
  function blockedByLegacyConversion(): boolean {
    if (pendingLegacyUnitConversion) {
      statusMessage = 'Still finalizing loaded settings — try saving again in a moment.';
      statusType = 'error';
      return true;
    }
    return false;
  }

  async function handleSave() {
    if (blockedByLegacyConversion()) return;
    if (!settingsPath) {
      return handleSaveAs();
    }
    try {
      await SaveSettings(settingsPath, serializeSettings() as any);
      addRecentSettings(settingsPath);
      statusMessage = `Saved to ${settingsPath}`;
      statusType = 'success';
    } catch (err: any) {
      statusMessage = `Save error: ${err}`;
      statusType = 'error';
    }
  }

  async function handleSaveAs() {
    if (blockedByLegacyConversion()) return;
    try {
      const path = await SaveSettingsDialog(serializeSettings() as any);
      if (path) {
        settingsPath = path;
        addRecentSettings(path);
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
          applySettings(result.settings, result.legacyAbsoluteUnits);
          addRecentSettings(path);
          statusMessage = `Loaded from ${result.path}`;
          statusType = 'success';
        }
      } else {
        await openInputModel(path);
      }
    } catch (err: any) {
      removeRecentSettings(path);
      removeRecentModel(path);
      statusMessage = `Open error: ${err}`;
      statusType = 'error';
    }
  }

  // File > Open: settings JSON only. The input model is chosen via the
  // dedicated control at the top of the settings panel (handleOpenModel).
  async function handleOpen() {
    const path = await OpenFileDialog();
    if (!path) return;
    await openFile(path);
  }

  async function handleOpenModel() {
    const path = await OpenModelDialog();
    if (!path) return;
    try {
      await openInputModel(path);
    } catch (err: any) {
      removeRecentModel(path);
      statusMessage = `Open error: ${err}`;
      statusType = 'error';
    }
  }

  function clearRecentSettings() {
    recentSettings = [];
    localStorage.removeItem('recentSettings');
  }

  function clearRecentModels() {
    recentModels = [];
    localStorage.removeItem('recentModels');
  }

  // Snapshot the pristine state declared by the $state initializers
  // above. Captured at script-init time, before any $effect or user
  // action mutates anything, so it reflects the factory defaults.
  // applySettings() rebuilds arrays/objects rather than aliasing, so
  // reusing this snapshot across resets is safe.
  const FACTORY_DEFAULTS = serializeSettings();

  // Correctness check: every persisted field MUST have a matching
  // entry in SETTINGS_SCHEMA, otherwise it would silently retain its
  // previous value across loads. Failing hard at module init means a
  // missing entry can't ship — the app refuses to boot until it's
  // fixed. Caveat: this only catches "in serializeSettings, missing
  // from schema." A new $state field that was never added to
  // serializeSettings won't appear in FACTORY_DEFAULTS at all and so
  // can't be checked here; that case is "just non-persistent" and
  // surfaces the moment the user tries to save and reload.
  {
    const handled = new Set(SETTINGS_SCHEMA.map(s => s.key));
    const missing = Object.keys(FACTORY_DEFAULTS).filter(k => !handled.has(k));
    if (missing.length > 0) {
      throw new Error(`SETTINGS_SCHEMA missing entries for: ${missing.join(', ')} — applySettings would not reset these on load`);
    }
  }

  function handleNew() {
    applySettings(FACTORY_DEFAULTS);
    // applySettings clears the viewport mesh + bbox automatically when
    // inputFile changes. Reset the rest of the transient UI state that
    // doesn't ride on persisted settings: in-progress sticker/pin
    // placement, the saved-as path, and any stale status message.
    placingStickerIndex = -1;
    pickingPinIndex = -1;
    settingsPath = '';
    statusMessage = '';
    statusType = 'idle';
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

  async function handleExportCollection() {
    const name = collectionStore.activeCollection;
    if (!name) return;
    try {
      await ExportCollection(name);
    } catch (err) {
      console.error('Failed to export collection:', err);
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
    inventoryCollectionColors = colors.map(c => ({ hex: c.hex, label: c.label, td: c.td }));
  }

  // Load initial inventory collection colors. Wrapped in untrack
  // because we explicitly want a one-shot read of the initial state
  // value at script init — subsequent changes are handled by
  // applySettings (load/reset paths) and the dropdown's onchange.
  untrack(() => loadInventoryCollectionColors(inventoryCollection));

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

  async function runPipeline(force = false) {
    if (!inputFile) {
      statusMessage = 'Please select an input file.';
      statusType = 'error';
      return;
    }
    // Allocate this run's id synchronously and replace the whole record in
    // one shot — phase, stages, error and output are reset atomically; no
    // scattered per-variable resets to forget. Input geometry is preserved
    // (run.input) so the input viewer keeps showing the current model while
    // it reprocesses; a new input-mesh event for this run overwrites it.
    // The previous output is carried forward as this run's *preview* (never
    // finalUrl — export/picking stay disabled): the backend only emits
    // output-preview-mesh when a geometry stage recomputes (cache miss), so
    // on a warm-geometry re-run (e.g. a colour-only change) it expects the
    // old output to stay on screen instead of flashing back to blank. The
    // progress overlay signals staleness while the run is in flight, and the
    // old /mesh/ blob stays valid until the backend stores its replacement.
    const id = ++runId;
    run = {
      id, phase: 'running', stages: [], error: '',
      input: { ...run.input },
      output: { previewUrl: run.output.finalUrl ?? run.output.previewUrl },
    };
    inputError = '';
    // The action bar shows "Recomputing…" from run.phase; no transient line.
    statusMessage = '';
    statusType = 'idle';
    resolvedUnlockedColors = [] as ColorInfo[];
    if (stageTimerHandle) {
      window.clearInterval(stageTimerHandle);
      stageTimerHandle = 0;
    }

    // ProcessPipeline enqueues the request and returns immediately. The
    // backend worker processes only the latest request and echoes this run
    // id (id) on every event so the exact-match gate can attribute results.
    await ProcessPipeline(settingsForBackend(), id, force, reloadSeq);
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

<svelte:window
  onclick={handleViewMenuOutside}
  onkeydown={(e) => {
    if (e.key === 'Escape') {
      if (triangleSelectMode) { triangleSelectMode = false; }
      if (cellSelectMode) { cellSelectMode = false; }
      viewMenuOpen = false;
      outputViewMenuOpen = false;
    }
  }}
/>

<Tooltip.Provider>

<main class="h-screen flex flex-col">
  <!-- Menu bar -->
  <Menubar.Root class="rounded-none border-b border-t-0 border-x-0">
    <Menubar.Menu>
      <Menubar.Trigger>File</Menubar.Trigger>
      <Menubar.Content>
        <Menubar.Item onSelect={handleNew}>New</Menubar.Item>
        <Menubar.Separator />
        <Menubar.Item onSelect={handleOpen}>Open JSON...</Menubar.Item>
        <Menubar.Sub>
          <Menubar.SubTrigger disabled={recentSettings.length === 0}>Open Recent JSON</Menubar.SubTrigger>
          <Menubar.SubContent align="start">
            {#each recentSettings as path}
              <Menubar.Item onSelect={() => openFile(path)}>
                {path.split(/[/\\]/).pop()}
                <span class="text-muted-foreground ml-auto pl-4 text-xs truncate max-w-48" title={path}>{shortenPath(path)}</span>
              </Menubar.Item>
            {/each}
            <Menubar.Separator />
            <Menubar.Item onSelect={clearRecentSettings}>Clear Recent</Menubar.Item>
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
    <Menubar.Menu>
      <Menubar.Trigger>Debug</Menubar.Trigger>
      <Menubar.Content>
        <Menubar.Item onSelect={() => { debugCellsDialogOpen = true; }} disabled={!outputMeshUrl || running}>
          View Cells…
        </Menubar.Item>
        <Menubar.Item onSelect={() => { triangleSelectMode = true; }} disabled={!inputMeshUrl && !outputMeshUrl}>
          Select Triangle…
        </Menubar.Item>
        <Menubar.Item onSelect={() => { cellSelectMode = true; }} disabled={!outputMeshUrl || running}>
          Select Cell…
        </Menubar.Item>
        <Menubar.Separator />
        <Menubar.CheckboxItem bind:checked={stats} closeOnSelect={false}>
          Stats
        </Menubar.CheckboxItem>
        <Menubar.CheckboxItem bind:checked={showSampledColors} closeOnSelect={false}>
          Show sampled colors
        </Menubar.CheckboxItem>
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
        <SettingsSection title="Model" summary={modelSummary}>
          {#snippet tip()}
            <HelpTip>
              Size the model, set a fallback color, and optionally repair geometry to clean up bad meshes.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <!-- Model file: one compact row. The select shows the current
                 file and doubles as the picker — "Browse for file…" opens the
                 native dialog and recent entries load instantly. Changing the
                 model swaps only the model: all other settings, stickers, and
                 color pins are kept (sticker placement is a fraction of the
                 model extent, so it rescales with the new model). -->
            <div class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 items-center">
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium">File</span>
                <HelpTip>
                  The 3D model to convert. Changing it swaps only the model —
                  all other settings, stickers, and color pins are kept.
                </HelpTip>
              </div>
              <Menubar.Root class="h-auto w-full min-w-0 gap-0 rounded-none border-0 bg-transparent p-0">
                <Menubar.Menu>
                  <Menubar.Trigger class="h-9 w-full min-w-0 justify-between gap-2 rounded-md border border-input bg-background px-2 font-normal {inputFile ? '' : 'text-muted-foreground'}">
                    <span class="truncate" title={inputFile}>
                      {inputFile ? inputFile.split(/[/\\]/).pop() : 'No model selected'}
                    </span>
                    <ChevronDownIcon class="size-4 shrink-0 opacity-60" />
                  </Menubar.Trigger>
                  <Menubar.Content align="start" class="min-w-56">
                    <Menubar.Item onSelect={handleOpenModel}>Browse for file…</Menubar.Item>
                    {#if recentModels.length > 0}
                      <Menubar.Sub>
                        <Menubar.SubTrigger>Recent</Menubar.SubTrigger>
                        <Menubar.SubContent class="max-w-80">
                          {#each recentModels as p}
                            <Menubar.Item onSelect={() => openInputModel(p)}>
                              {p.split(/[/\\]/).pop()}
                              <span class="text-muted-foreground ml-auto pl-4 text-xs truncate max-w-48" title={p}>{shortenPath(p)}</span>
                            </Menubar.Item>
                          {/each}
                          <Menubar.Separator />
                          <Menubar.Item onSelect={clearRecentModels}>Clear recent</Menubar.Item>
                        </Menubar.SubContent>
                      </Menubar.Sub>
                    {/if}
                  </Menubar.Content>
                </Menubar.Menu>
              </Menubar.Root>
            </div>

            <!-- Size / Scale on its own row -->
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
              {#if sizeMode === 'size'}
                <Input id="size" bind:value={sizeValue} type="number" step={1} />
              {:else}
                <Input id="scale" bind:value={scaleValue} type="number" step={0.1} />
              {/if}
            </div>

            <!-- Base color: label spans both columns (matching the Printer section's
                 layout); below it, the solid/texture toggle on the left and the
                 picker for the chosen mode on the right. -->
            <div class="grid grid-cols-2 gap-x-4 gap-y-2 items-end">
              <div class="col-span-2 flex items-center gap-1.5">
                <span class="text-sm font-medium">Base color</span>
                <HelpTip>
                  Color used for faces that aren't covered by the model's texture. Pick a single color, or load a MaterialX (.mtlx / .zip) graph for a procedural or image-backed pattern.
                </HelpTip>
              </div>
              <div class="flex items-center gap-3">
                <label class="flex items-center gap-1.5 text-sm">
                  <input type="radio" name="bcmode" value="solid" checked={baseColorMode === 'solid'} onchange={() => { baseColorMode = 'solid'; }} />
                  Solid
                </label>
                <label class="flex items-center gap-1.5 text-sm">
                  <input type="radio" name="bcmode" value="texture" checked={baseColorMode === 'texture'} onchange={() => { baseColorMode = 'texture'; baseColorPickerOpen = false; }} />
                  Texture
                </label>
              </div>
              {#if baseColorMode === 'solid'}
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
                    Pick color (default if unset)
                  </Button>
                {/if}
              {:else}
                {#if baseMaterialXPath}
                  <div class="flex items-center gap-2">
                    <span
                      class="h-9 flex-1 rounded border bg-muted text-xs flex items-center px-2 truncate"
                      title={baseMaterialXPath}
                    >
                      {baseMaterialXPath.split(/[\\/]/).pop()}
                    </span>
                    <Button variant="ghost" size="sm" onclick={() => { baseMaterialXPath = ''; }}>Clear</Button>
                  </div>
                {:else}
                  <Button variant="outline" class="w-full" size="sm" onclick={async () => {
                    const r = await OpenMaterialXFile();
                    if (r && r.path) {
                      baseMaterialXPath = r.path;
                      await validateMaterialXFile(r.path);
                    }
                  }}>Load .mtlx / .zip</Button>
                {/if}
              {/if}
            </div>

            <!-- Persistent error banner for an unloadable / unsupported
                 MaterialX file. Spans the row (col-span-2 in the
                 surrounding grid-cols-2 base-color block) and uses the
                 destructive theme tokens so it reads as "the texture
                 you picked is NOT being applied" — the failure mode
                 the user reported was missing the status-bar warning
                 entirely and assuming the texture was working. -->
            {#if baseColorMode === 'texture' && baseMaterialXPath && baseMaterialXError}
              <div class="rounded border border-destructive bg-destructive/10 text-destructive text-xs px-3 py-2">
                <div class="font-medium">MaterialX texture not applied</div>
                <div class="mt-1 break-words">{baseMaterialXError}</div>
              </div>
            {/if}

            {#if baseColorMode === 'solid' && baseColorPickerOpen}
              <div>
                <CollectionPicker
                  onselect={(hex, label, collection, td) => {
                    baseColor = { hex, label, collection, td };
                    baseColorPickerOpen = false;
                  }}
                  onclose={() => { baseColorPickerOpen = false; }}
                />
              </div>
            {/if}

            {#if baseColorMode === 'texture' && baseMaterialXPath}
              <div class="grid grid-cols-2 gap-x-4 gap-y-2 items-end">
                <div class="flex items-center gap-1.5">
                  <span class="text-sm font-medium">Tile size</span>
                  <HelpTip>
                    Object-space scale (mm per shading-unit cycle) applied before sampling. Smaller = denser pattern. For image-backed packs this is also the texture's repeat distance.
                  </HelpTip>
                </div>
                <div class="flex items-center gap-2">
                  <Input value={baseMaterialXTileDisplayMM} oninput={(e) => setBaseMaterialXTileMM(parseFloat(e.currentTarget.value))} type="number" min={0.1} step={0.5} class="flex-1" />
                  <span class="text-xs text-muted-foreground">mm</span>
                </div>
                <div class="flex items-center gap-1.5">
                  <span class="text-sm font-medium">Projection sharpness</span>
                  <HelpTip>
                    Sharpness of the triplanar projection blend for image-backed MaterialX. 1 is a soft cosine blend; higher values approach a hard box map. Ignored by procedural .mtlx that don't read texture coordinates.
                  </HelpTip>
                </div>
                <Input bind:value={baseMaterialXTriplanarSharpness} type="number" min={0.5} max={32} step={0.5} class="flex-1" />
              </div>
            {/if}

            <!-- Repair geometry -->
            <div class="space-y-2">
              <div class="flex items-center gap-2 text-sm font-medium">
                <span>Repair geometry</span>
                <HelpTip>
                  Rebuild a watertight mesh before slicing to fix models with holes, self-intersections, thin walls, or inverted normals. "None" uses the model as-is. "Winding-number remesh" is medium speed and fixes most broken meshes, though very fine detail may be resampled away. "Alpha wrap" is slow but most robust — it shrink-wraps a watertight shell and bridges small gaps and pockets.
                </HelpTip>
                <select
                  class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm ml-auto"
                  bind:value={meshRepair}
                >
                  <option value="none">None</option>
                  <option value="fwn">Winding-number remesh</option>
                  <option value="alphawrap">Alpha wrap</option>
                </select>
              </div>
              <p class="text-xs text-muted-foreground">
                {#if meshRepair === 'fwn'}
                  Rebuilds the surface on a fine grid — medium speed; fixes most broken meshes but may lose very fine detail.
                {:else if meshRepair === 'alphawrap'}
                  Shrink-wraps a watertight shell — slow but most robust; bridges small gaps and pockets.
                {:else}
                  The model is used as-is. Pick a repair method for damaged or non-watertight meshes.
                {/if}
              </p>
              {#if meshRepair === 'fwn'}
                <div class="grid grid-cols-2 gap-3 pl-6 text-sm">
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Detail XY (mm)
                      <HelpTip>
                        Grid pitch in the horizontal plane; auto = nozzle diameter. Smaller keeps more detail, slower.
                      </HelpTip>
                    </span>
                    <input type="number" step="0.1" min="0"
                           placeholder={`auto (${(parseFloat(nozzleDiameter) || 0.4).toFixed(2)})`}
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={fwnDetailXY} />
                    <span class="text-xs text-muted-foreground">Smaller keeps more detail; larger is faster.</span>
                  </label>
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Detail Z (mm)
                      <HelpTip>
                        Vertical grid pitch; auto = layer height. Smaller keeps more detail, slower.
                      </HelpTip>
                    </span>
                    <input type="number" step="0.1" min="0"
                           placeholder={`auto (${(parseFloat(layerHeight) || 0.2).toFixed(2)})`}
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={fwnDetailZ} />
                    <span class="text-xs text-muted-foreground">Smaller keeps more detail; larger is faster.</span>
                  </label>
                </div>
              {:else if meshRepair === 'alphawrap'}
                <div class="grid grid-cols-2 gap-3 pl-6 text-sm">
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Detail size (mm)
                      <HelpTip>
                        The radius of the probing sphere. Larger = smoother/coarser result that bridges gaps but loses detail; smaller = hugs the surface more tightly (and is slower).
                      </HelpTip>
                    </span>
                    <input type="number" step="0.1" min="0"
                           placeholder={`auto (${(parseFloat(nozzleDiameter) || 0.4).toFixed(2)})`}
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={alphaWrapAlpha} />
                    <span class="text-xs text-muted-foreground">Larger bridges gaps; smaller keeps more detail.</span>
                  </label>
                  <label class="flex flex-col gap-1">
                    <span class="text-muted-foreground flex items-center gap-1.5">
                      Surface offset (mm)
                      <HelpTip>
                        How far the wrap sits above the input surface. Larger values shrink-wrap less tightly.
                      </HelpTip>
                    </span>
                    <input type="number" step="0.01" min="0"
                           placeholder="auto (alpha / 30)"
                           class="h-9 rounded border bg-background text-foreground px-2"
                           bind:value={alphaWrapOffset} />
                    <span class="text-xs text-muted-foreground">How far the shell sits above the surface; larger wraps less tightly.</span>
                  </label>
                </div>
              {/if}
            </div>

            <!-- Fine tuning: geometry-pipeline toggles moved from the old
                 Advanced section. They affect mesh output, so they live at
                 the bottom of Model. Default closed. -->
            <SettingsSection title="Fine tuning" variant="sub" open={false}>
              <div class="flex flex-wrap gap-x-6 gap-y-3">
                <label class="flex items-center gap-2 text-sm">
                  <Checkbox bind:checked={noMerge} />
                  No coplanar merge
                  <HelpTip>
                    Skip merging coplanar same-color triangles into larger polygons after clipping. Produces more triangles but keeps the raw clipped geometry.
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
                  <Checkbox bind:checked={noCellMerge} />
                  No cell merge
                  <HelpTip>
                    Disable merging: clip every cell individually instead of pairing adjacent same-color cells within each layer and clipping them together. Merging (the default) is faster, with fewer output triangles and no internal seams between same-color cells, and does not change colors. Tick this only to force the per-cell clip.
                  </HelpTip>
                </label>
              </div>
            </SettingsSection>
          </div>
        </SettingsSection>

        <SettingsSection title="Appearance" summary={appearanceSummary}>
          {#snippet tip()}
            <HelpTip>
              Choose the filament colors, the dithering algorithm, and the color
              adjustments applied before dithering.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <!-- Filament slots + collection picker -->
            <div class="space-y-4">
              <!-- Filament slot rows: [swatch] [name/hex] [TD] [Auto/Locked] [x] -->
              <div class="space-y-1.5">
                {#each colorSlots as slot, i}
                  {@const resolved = resolvedBySlot[i]}
                  {@const info = slot ?? resolved}
                  {@const usage = slotUsage[i]}
                  {@const unused = slot !== null && usage !== null && usage.triangles === 0}
                  <div class="flex items-center gap-2 {unused ? 'opacity-60' : ''}">
                    <!-- Swatch + name/hex: click to open the collection picker -->
                    <button
                      type="button"
                      class="flex flex-1 min-w-0 items-center gap-2 rounded px-1 py-1 text-left cursor-pointer hover:bg-muted/50 transition-colors {pickerIndex === i ? 'ring-2 ring-primary' : ''}"
                      title={info ? colorTooltip(info) : 'auto'}
                      onclick={() => openPicker(i)}
                    >
                      <span
                        class="shrink-0 w-6 h-6 rounded shadow-[inset_0_0_0_1px_rgba(0,0,0,0.15)] {info ? '' : 'bg-muted'} {slot ? 'border border-border' : 'border border-dashed border-muted-foreground/60'}"
                        style={info ? `background: ${info.hex};` : ''}
                      ></span>
                      <span class="flex min-w-0 flex-col leading-tight">
                        <span class="truncate text-sm text-foreground">{info ? (info.label || info.hex) : 'auto'}</span>
                        {#if slot}
                          <span class="truncate text-[11px] text-muted-foreground">{info?.hex}</span>
                        {:else if resolved}
                          <span class="text-[11px] text-muted-foreground">auto</span>
                        {/if}
                      </span>
                      {#if info?.td}
                        <span class="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground" title="Filament transmission distance">TD {info.td}</span>
                      {/if}
                      {#if unused}
                        <span class="shrink-0 rounded bg-yellow-500/15 px-1.5 py-0.5 text-[10px] font-semibold text-yellow-600 dark:text-yellow-400" title="This locked color was unused in the last run">!</span>
                      {/if}
                    </button>
                    <!-- Usage bar: fraction of the last run's output triangles
                         assigned to this color. Empty gutter before any run. -->
                    <div class="shrink-0 w-14 flex flex-col items-end gap-0.5">
                      {#if usage}
                        {@const frac = totalUsageTriangles > 0 ? usage.triangles / totalUsageTriangles : 0}
                        <div class="w-full h-1.5 rounded bg-muted overflow-hidden" title="{usage.triangles.toLocaleString()} triangles">
                          <div class="h-full rounded bg-primary" style="width: {(frac * 100).toFixed(1)}%"></div>
                        </div>
                        <span class="text-[10px] leading-none text-muted-foreground tabular-nums">{Math.round(frac * 100)}%</span>
                      {/if}
                    </div>
                    <!-- Auto/Locked toggle: flips the slot's locked vs auto state -->
                    <button
                      type="button"
                      class="shrink-0 flex items-center gap-1 rounded border px-2 py-1 text-[11px] cursor-pointer hover:bg-muted/50 transition-colors disabled:cursor-default disabled:opacity-50 {slot ? 'text-foreground' : 'text-muted-foreground'}"
                      title={slot ? 'Locked — click to set to auto' : 'Auto — click to lock this resolved color'}
                      disabled={!slot && !resolved}
                      onclick={() => toggleLock(i)}
                    >
                      {#if slot}
                        <LockIcon size={12} /> Locked
                      {:else}
                        <LockOpenIcon size={12} /> Auto
                      {/if}
                    </button>
                    <!-- Remove slot -->
                    <button
                      type="button"
                      class="shrink-0 w-6 h-6 rounded flex items-center justify-center text-lg leading-none text-muted-foreground hover:text-destructive hover:bg-muted/50 transition-colors {colorSlots.length > 1 ? '' : 'invisible'}"
                      title="Remove this color"
                      onclick={() => removeColorSlot(i)}
                    >&times;</button>
                  </div>
                {/each}
                <!-- Trailing add-color row (replaces the dashed + tile) -->
                {#if colorSlots.length < 16}
                  <button
                    type="button"
                    class="w-full rounded border border-dashed border-muted-foreground/30 flex items-center justify-center gap-1 py-1.5 text-sm text-muted-foreground hover:border-muted-foreground/60 hover:text-foreground transition-colors cursor-pointer"
                    onclick={addColorSlot}
                  >+ Add color</button>
                {/if}
                {#if pickerIndex !== null}
                  <CollectionPicker
                    onselect={pickColor}
                    onclose={closePicker}
                  />
                {/if}
                {#if unusedLockedCount > 0}
                  <p class="text-xs text-yellow-600 dark:text-yellow-400">
                    {unusedLockedCount} {unusedLockedCount === 1 ? 'color' : 'colors'} unused in the last run — consider removing {unusedLockedCount === 1 ? 'it' : 'them'}.
                  </p>
                {/if}
              </div>

              <!-- Auto-fill collection source -->
              <div class="space-y-1">
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
                <p class="text-xs text-muted-foreground">Auto slots are filled from this collection.</p>
              </div>
            </div>

            <!-- Dither mode + per-mode tuning -->
            <div class="space-y-4">
              <div class="space-y-2">
                <div class="flex items-center gap-1.5">
                  <Label for="dither">Mode</Label>
                  <HelpTip>
                    "Dizzy damped" (default) is randomized error-diffusion (Liam Appelbe's blue-noise dizzy) iterated with a localized, damped drift correction — blue-noise look with no directional structure and each color region's average kept true. "Floyd-Steinberg" uses a deterministic scanline order that preserves average chroma exactly, at the cost of visible directional structure on flat areas. "Riemersma" walks cells along a locally-coherent tour through the surface and diffuses each cell's error into a sliding window of recent cells — preserves chroma without scanline directionality. "Blue noise" picks the smallest palette simplex (pair, triangle, or full) that brackets each cell's input within a fixed tolerance, then chooses among its vertices via a low-discrepancy sequence — bounds wander on uniform regions at the cost of a small global drift. "None" disables dithering and snaps each cell to the nearest palette color. Previews are approximate — they show image-space dithering of a snapshot of the loaded model, not the actual surface-cell dithering the pipeline applies.
                  </HelpTip>
                </div>
                <div class="grid grid-cols-2 gap-2 sm:grid-cols-3">
                  {#each DITHER_OPTIONS as opt}
                    <button
                      type="button"
                      onclick={() => (dither = opt.value)}
                      aria-pressed={dither === opt.value}
                      class="flex flex-col overflow-hidden rounded-md border bg-card text-left transition-colors hover:border-primary focus-visible:outline-none {dither === opt.value ? 'ring-2 ring-primary' : ''}"
                    >
                      <!-- bg-muted/30 matches the model viewers' background
                           (same token, and --card === --background in both
                           themes) so a transparent live preview reads as the
                           viewer background with the dithered model on top; a
                           theme switch needs no regeneration. The opaque static
                           fallback PNGs fully cover it. -->
                      <img
                        src={ditherThumbs?.[opt.value] ?? DITHER_META[opt.value]?.thumb}
                        alt="{opt.label} dither preview"
                        class="aspect-[3/2] w-full object-cover bg-muted/30"
                        style="image-rendering: pixelated;"
                        draggable="false"
                      />
                      <div class="px-2 py-1.5">
                        <div class="text-xs font-medium leading-tight">{opt.label}</div>
                        <div class="text-[11px] leading-tight text-muted-foreground">{DITHER_META[opt.value]?.tagline}</div>
                      </div>
                    </button>
                  {/each}
                </div>
              </div>

              {#if dither === 'riemersma'}
                <div class="space-y-1">
                  <div class="flex items-center justify-between">
                    <div class="flex items-center gap-1.5">
                      <Label>Alpha</Label>
                      <HelpTip>
                        Per-cell input-bias maximum (0..1). Pulls each cell's palette pick toward its nearest-input palette when the cell's input is close to a palette color. 0 = pure Riemersma (zero average drift but black/white oscillation around near-grey input). Higher values suppress that oscillation by preferring the close-input palette; too high (≥0.9) starts to posterize textured surfaces. 0.85 is the default.
                      </HelpTip>
                    </div>
                    <span class="text-xs text-muted-foreground w-10 text-right">{riemersmaBias.toFixed(2)}</span>
                  </div>
                  <Slider type="single" min={0} max={1} step={0.05} value={riemersmaBias} onValueChange={(v: number) => riemersmaBias = v} onValueCommit={(v: number) => committedRiemersmaBias = v} />
                  <p class="text-xs text-muted-foreground">Higher suppresses black/white flicker on flat areas; too high posterizes.</p>
                </div>
              {/if}

              <!-- The "Blue noise" mode (bn-adapt-5) pins its bracket
                   tolerance to 5, so it exposes no tuning slider. The
                   blueNoiseTol setting is retained for compatibility but
                   no longer affects production output. -->
            </div>

            <!-- Color adjustments -->
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
                    <Label>Color similarity threshold</Label>
                    <HelpTip>
                      CIELAB distance below which pixels snap to the nearest palette color instead of being dithered. Lower values preserve more color detail; higher values reduce dithering artifacts.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-8 text-right">{colorSnap}</span>
                </div>
                <Slider type="single" min={0} max={50} step={1} value={colorSnap} onValueChange={(v: number) => colorSnap = v} onValueCommit={(v: number) => committedColorSnap = v} />
                <p class="text-xs text-muted-foreground">Higher = fewer speckles, less color detail.</p>
              </div>
            </div>

            <!-- Fine tuning: color / dither quality knobs moved from the old
                 Advanced section. Default closed. -->
            <SettingsSection title="Fine tuning" variant="sub" open={false}>
              <div class="flex flex-wrap gap-x-6 gap-y-3">
                <label class="flex items-center gap-2 text-sm">
                  <Checkbox bind:checked={colorAwareCells} />
                  Color-aware cells
                  <HelpTip>
                    Segment each layer by color and tile each monochrome region separately, so cell boundaries land on color boundaries. Sharp patterns (e.g. a checkerboard) stay pure black/white instead of averaging to gray at the edges. Color features smaller than one cell are merged away. On by default.
                  </HelpTip>
                </label>
                {#if colorAwareCells}
                  <div class="space-y-1 pl-6">
                    <div class="flex items-center justify-between">
                      <div class="flex items-center gap-1.5">
                        <Label>Edge sharpness threshold</Label>
                        <HelpTip>
                          Cut a color boundary into a cell boundary only where neighboring surface colors differ by more than this CIELAB distance. Low (~5) cuts almost any edge; higher (~20-30) ignores soft shading and cuts only crisp edges.
                        </HelpTip>
                      </div>
                      <span class="text-xs text-muted-foreground w-8 text-right">{colorRegionContrast}</span>
                    </div>
                    <Slider type="single" min={0} max={50} step={1} value={colorRegionContrast} onValueChange={(v: number) => colorRegionContrast = v} onValueCommit={(v: number) => committedColorRegionContrast = v} />
                    <p class="text-xs text-muted-foreground">Higher cuts cell boundaries only on crisp edges; lower cuts almost any edge.</p>
                  </div>
                {/if}
                <label class="flex items-center gap-2 text-sm">
                  <Checkbox bind:checked={regionDither} />
                  Confine dither to color regions
                  <HelpTip>
                    Advanced. Stop dither error from bleeding across color boundaries: the cell graph is split into color regions and each is dithered in isolation, so a gray area's error can't speckle an adjacent solid black or white area. Smooth gradients still diffuse normally; only sharp color jumps act as barriers. Independent of color-aware cells; works with every dither mode. Off by default.
                  </HelpTip>
                </label>
                {#if regionDither}
                  <div class="space-y-1 pl-6">
                    <div class="flex items-center justify-between">
                      <div class="flex items-center gap-1.5">
                        <Label>Region barrier threshold</Label>
                        <HelpTip>
                          Treat neighboring cells as different color regions (a barrier to error) only where their colors differ by more than this CIELAB distance. Low (~5) confines almost everywhere; higher (~20-30) only blocks crisp edges while letting soft shading diffuse.
                        </HelpTip>
                      </div>
                      <span class="text-xs text-muted-foreground w-8 text-right">{regionDitherDeltaE}</span>
                    </div>
                    <Slider type="single" min={0} max={50} step={1} value={regionDitherDeltaE} onValueChange={(v: number) => regionDitherDeltaE = v} onValueCommit={(v: number) => committedRegionDitherDeltaE = v} />
                    <p class="text-xs text-muted-foreground">Higher blocks dither bleed only at sharp color jumps; lower confines almost everywhere.</p>
                  </div>
                {/if}
                <label class="flex items-center gap-2 text-sm">
                  <Checkbox bind:checked={rejectColorOutliers} />
                  Reject color outliers
                  <HelpTip>
                    When almost all of a cell's color samples agree (one color holds at least 75% of them), drop the stray 1-2 samples that strayed across a color boundary into the cell instead of letting them pull the cell's averaged color. Genuinely mixed cells (no clear majority) keep every sample, so dithering is unaffected. On by default.
                  </HelpTip>
                </label>
                <label class="flex items-center gap-2 text-sm">
                  <Checkbox bind:checked={honorTD} />
                  Translucency-aware mixing
                  <HelpTip>
                    Opacity-weight the dither by each filament's transmission distance (TD). Translucent filaments (high TD) cover more area to deliver the same perceived color, so e.g. a transparent yellow no longer disappears into an opaque red. On by default; untick to use the plain area-weighted mix (treat every filament as opaque).
                  </HelpTip>
                </label>
                <p class="w-full text-xs text-muted-foreground">Gives translucent filaments more area so they aren't lost under opaque ones.</p>
                {#if honorTD}
                  <div class="ml-1 pl-3 border-l border-border space-y-3">
                    <div class="flex items-center gap-2 text-sm">
                      <span>Translucency model</span>
                      <HelpTip>
                        "Area compensation" is the legacy opacity-weighted mix (gives translucent filaments more area). "Layered (infill-aware)" instead estimates the color the eye actually sees once light leaks through the finite shell into the infill filament, and dithers against those effective colors. The shell thickness it integrates over is derived from the selected printer's wall settings (wall loops × line widths) — the same process profile written into the exported 3MF — so it always matches how the model will actually be sliced. See docs/td-translucency-model.md.
                      </HelpTip>
                      <select
                        class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm ml-auto"
                        bind:value={tdModel}
                      >
                        <option value="">Area compensation</option>
                        <option value="layered">Layered (infill-aware)</option>
                      </select>
                    </div>
                    {#if tdModel === 'layered'}
                      <div class="flex items-center gap-2 text-sm">
                        <span>Infill color</span>
                        <HelpTip>
                          The filament that prints the infill/inner walls. Translucent surface colors wash toward this. White maximizes chroma headroom.
                        </HelpTip>
                        <input type="color" class="ml-auto h-9 w-12 rounded-md border border-input bg-background" bind:value={infillColor} />
                      </div>
                    {/if}
                  </div>
                {/if}
              </div>
            </SettingsSection>
          </div>
        </SettingsSection>

        <SettingsSection title="Modify" open={false} summary={modifySummary}>
          {#snippet tip()}
            <HelpTip>
              Stamp stickers onto the surface, or split the model into printable halves.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <div>
              <div class="flex items-center gap-2 mb-2">
                <span class="text-xs font-medium text-muted-foreground">Stickers</span>
                <HelpTip>
                  Stamp logos, labels, or artwork onto the model surface.
                </HelpTip>
              </div>
              <div class="ml-1 pl-3 border-l border-border">
                <StickerPanel
                  bind:stickers={stickers}
                  bind:placingIndex={placingStickerIndex}
                  extentMM={scaledMaxExtentMM ?? 0}
                  onAdd={addSticker}
                  onRemove={removeSticker}
                />
              </div>
            </div>
            <div>
              <div class="flex items-center gap-2 mb-2">
                <span class="text-xs font-medium text-muted-foreground">Split</span>
                <HelpTip>
                  Cut the model into two halves that print side by side
                  and assemble back together with peg/pocket alignment.
                  Useful for build-volume limits, or to expose supports
                  that would otherwise be hard to remove.
                </HelpTip>
              </div>
              <div class="ml-1 pl-3 border-l border-border">
                <SplitControls
                  bind:enabled={splitEnabled}
                  bind:axis={splitAxis}
                  bind:offset={splitOffset}
                  bind:tiltA={splitTiltA}
                  bind:tiltB={splitTiltB}
                  bind:connectorStyle={splitConnectorStyle}
                  bind:connectorCount={splitConnectorCount}
                  bind:connectorDiamMM={splitConnectorDiamMM}
                  bind:connectorDepthMM={splitConnectorDepthMM}
                  bind:clearanceMM={splitClearanceMM}
                  bind:orientationA={splitOrientationA}
                  bind:orientationB={splitOrientationB}
                  extentMM={scaledMaxExtentMM ?? 0}
                  minOffset={splitOffsetMin}
                  maxOffset={splitOffsetMax}
                  onRepairForced={() => { if (meshRepair === 'none') meshRepair = 'alphawrap'; }}
                />
              </div>
            </div>
          </div>
        </SettingsSection>

        <SettingsSection title="Print setup" open={false} summary={printSummary}>
          {#snippet tip()}
            <HelpTip>
              Target hardware. Sets the smallest detail the output can reproduce.
            </HelpTip>
          {/snippet}
          <div class="space-y-6">
            <!-- Printer / Nozzle / Layer share one row. Three explicit
                 columns: printer flexes (minmax(0, 1fr) so it can shrink
                 below the longest printer name's intrinsic min-content),
                 nozzle is a fixed 6.5em (clears "0.4mm" + chevron),
                 layer is 7.5em so it ends up exactly 1em wider than
                 nozzle (clears "0.20mm" + chevron with a hair of
                 breathing room). Em-sized rather than fr-sized so the
                 layer-minus-nozzle delta stays at 1em regardless of
                 sidebar width, and so widening one doesn't steal from
                 the others. -->
            <div class="grid grid-cols-[minmax(0,1fr)_6.5em_7.5em] gap-x-3 gap-y-2 items-end">
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium">Printer</span>
                <HelpTip>
                  Target printer for the exported 3MF. Nozzle and layer height
                  options adapt to what the selected printer supports.
                </HelpTip>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium">Nozzle</span>
                <HelpTip>
                  Nozzle diameter variant for the selected printer. Also sets the
                  finest horizontal detail the output can represent.
                </HelpTip>
              </div>
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium">Layer</span>
                <HelpTip>
                  Layer height for the print. Must match the layer height used
                  when slicing.
                </HelpTip>
              </div>
              <select
                class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
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
              <!-- Option labels carry the "mm" unit so the column header
                   doesn't have to. value= stays the bare numeric string
                   because nozzleDiameter/layerHeight in state are bare
                   numbers (and round-trip through the settings JSON). -->
              <select
                class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
                bind:value={nozzleDiameter}
                onchange={() => reconcilePrinterSelection()}
              >
                {#if currentPrinter}
                  {#each currentPrinter.nozzles as n (n.diameter)}
                    <option value={n.diameter}>{n.diameter}mm</option>
                  {/each}
                {:else}
                  <option value={nozzleDiameter}>{nozzleDiameter}mm</option>
                {/if}
              </select>
              <select
                class="h-9 rounded-md border border-input bg-background text-foreground px-2 text-sm"
                bind:value={layerHeight}
              >
                {#if currentNozzle}
                  {#each currentNozzle.layerHeights as lh (lh)}
                    <option value={fmtLayerHeight(lh)}>{fmtLayerHeight(lh)}mm</option>
                  {/each}
                {:else}
                  <option value={layerHeight}>{layerHeight}mm</option>
                {/if}
              </select>
            </div>
            <p class="text-xs text-muted-foreground">Layer must match the layer height you slice with.</p>

            <!-- Layer XY scale sliders moved from the old Advanced section.
                 Set-once print-tuning knobs, so they live in Print setup. -->
            <div class="space-y-4">
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-1.5">
                    <Label>First-layer blob size</Label>
                    <HelpTip>
                      Multiplier on layer-0 voxel cell XY size for bed adhesion. 1 = no enlargement; higher values produce bigger first-layer color blobs that stick to the bed but coarsen the first layer's color resolution.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-10 text-right">{layer0AdhesionXYScale.toFixed(1)}</span>
                </div>
                <Slider type="single" min={1} max={15} step={0.5} value={layer0AdhesionXYScale} onValueChange={(v: number) => layer0AdhesionXYScale = v} onValueCommit={(v: number) => committedLayer0AdhesionXYScale = v} />
                <p class="text-xs text-muted-foreground">Higher = bigger first-layer blobs for bed adhesion, coarser first-layer color.</p>
              </div>
              <div class="space-y-1">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-1.5">
                    <Label>Color grid coarseness</Label>
                    <HelpTip>
                      Multiplier on upper-layer voxel cell XY size relative to the slicer's line width. Lower values pack more color detail into each layer; higher values coarsen the grid in exchange for fewer primitives. Below ~1.20 the slicer visibly drops detail on vertical walls (and sometimes elsewhere), so values below that often don't make it onto the print.
                    </HelpTip>
                  </div>
                  <span class="text-xs text-muted-foreground w-10 text-right">{upperLayerXYScale.toFixed(2)}</span>
                </div>
                <Slider type="single" min={1} max={4} step={0.05} value={upperLayerXYScale} onValueChange={(v: number) => upperLayerXYScale = v} onValueCommit={(v: number) => committedUpperLayerXYScale = v} />
                <p class="text-xs text-muted-foreground">Lower packs in finer color detail; below ~1.20 the slicer may drop detail.</p>
              </div>
            </div>
          </div>
        </SettingsSection>
      </Card.Content>
    </Card.Root>

    </div>

    <!-- Persistent action bar pinned to the bottom of the left panel: run
         state (from the run lifecycle record, so it inherits stale-run
         gating) plus a primary Export 3MF button duplicating the File menu
         item. -->
    <div class="shrink-0 border-t border-border bg-background px-6 py-3 space-y-1.5">
      <div class="flex items-center gap-3">
        <span class="flex-1 min-w-0 flex items-center gap-1.5 text-sm {runStateClass}">
          {#if run.phase === 'running'}
            <svg class="size-3.5 shrink-0 animate-spin text-muted-foreground" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
            </svg>
          {/if}
          <span class="truncate">{runStateLabel}</span>
        </span>
        <Button size="sm" onclick={exportTo3MF} disabled={!outputMeshUrl || running || saving}>
          Export 3MF
        </Button>
      </div>
      {#if actionDetail}
        <p class="text-xs {statusType === 'success' ? 'text-green-500' : statusType === 'error' ? 'text-red-500' : statusType === 'warning' ? 'text-yellow-500' : 'text-muted-foreground'}">{actionDetail}</p>
      {/if}
    </div>
  </div>

  <!-- Right column: 3D viewers -->
  <div class="flex-1 flex flex-col p-4 gap-4 min-w-0">
    <div class="flex-1 min-h-0 relative">
      <ModelViewer
        meshUrl={inputViewMode === 'wrapped' && wrappedMeshUrl ? wrappedMeshUrl : inputMeshUrl}
        overlayMeshUrl={inputViewMode === 'wrapped' ? undefined : inputOverlayMeshUrl}
        label={inputFile ? `${inputViewMode === 'wrapped' ? 'Repaired Model: ' : 'Input Model: '}${shortenPath(inputFile)}` : 'Input Model'}
        viewerId="input" camera={sharedCamera} {brightness} {contrast} {saturation}
        pickMode={inputViewMode === 'input' && pickingPinIndex >= 0}
        pickTriangleMode={triangleSelectMode}
        stickerPlaceMode={inputViewMode === 'input' && placingStickerIndex >= 0}
        stickerImage={placingSticker?.thumbnail ?? ''}
        stickerSize={(placingSticker?.scale ?? 0) * (nativeExtentMM ?? 0)}
        stickerRotation={placingSticker?.rotation ?? 0}
        onColorPick={handleColorPick}
        onTrianglePick={handleTrianglePick}
        onStickerPlace={handleStickerPlace}
        warpPins={inputViewMode === 'input' && pickingPinIndex < 0 ? warpPins : []}
        loading={inputFile ? inputFile.split('/').pop() ?? '' : ''}
        errorMessage={inputError}
        cutPlane={cutPlanePreview}
        viewMode={inputViewStyle}
        onCaptureReady={(fn) => inputCapture = fn}
      />
      <div
        bind:this={viewMenuRef}
        class="absolute top-2 right-2 z-10 text-xs"
      >
        <button
          type="button"
          class="px-2 py-1 rounded border border-border bg-background/90 hover:bg-muted shadow-sm"
          aria-haspopup="true"
          aria-expanded={viewMenuOpen}
          onclick={() => { viewMenuOpen = !viewMenuOpen; }}
        >View</button>
        {#if viewMenuOpen}
          <div class="absolute top-full right-0 mt-1 min-w-[10rem] rounded border border-border bg-popover shadow-md overflow-hidden">
            {#if wrappedMeshUrl}
              <div class="px-3 pt-1.5 text-[11px] font-semibold uppercase tracking-widest text-muted-foreground">Source</div>
              <button
                type="button"
                class="block w-full text-left px-3 py-1.5 hover:bg-muted {inputViewMode === 'input' ? 'font-medium bg-muted/60' : ''}"
                onclick={() => { inputViewMode = 'input'; viewMenuOpen = false; }}
              >Input model</button>
              <button
                type="button"
                class="block w-full text-left px-3 py-1.5 hover:bg-muted {inputViewMode === 'wrapped' ? 'font-medium bg-muted/60' : ''}"
                onclick={() => { inputViewMode = 'wrapped'; viewMenuOpen = false; }}
              >Repaired</button>
              <div class="border-t border-border"></div>
            {/if}
            <div class="px-3 pt-1.5 text-[11px] font-semibold uppercase tracking-widest text-muted-foreground">Style</div>
            <label class="flex items-center gap-2 px-3 py-1.5 hover:bg-muted cursor-pointer">
              <input type="radio" name="input-view-style" bind:group={inputViewStyle} value="solid" />
              <span>Solid</span>
            </label>
            <label class="flex items-center gap-2 px-3 py-1.5 hover:bg-muted cursor-pointer">
              <input type="radio" name="input-view-style" bind:group={inputViewStyle} value="hidden-line" />
              <span>Hidden line</span>
            </label>
          </div>
        {/if}
      </div>
    </div>
    <div class="flex-1 min-h-0 relative">
      <ModelViewer meshUrl={outputMesh} label="Output Model" viewerId="output" camera={sharedCamera} stages={pipelineStages} {stageTick} progressActive={running} {pipelineError} viewMode={outputViewStyle} pickTriangleMode={triangleSelectMode} onTrianglePick={handleTrianglePick} pickCellMode={cellSelectMode} onCellPick={handleCellPick} />
      <div
        bind:this={outputViewMenuRef}
        class="absolute top-2 right-2 z-10 text-xs"
      >
        <button
          type="button"
          class="px-2 py-1 rounded border border-border bg-background/90 hover:bg-muted shadow-sm"
          aria-haspopup="true"
          aria-expanded={outputViewMenuOpen}
          onclick={() => { outputViewMenuOpen = !outputViewMenuOpen; }}
        >View</button>
        {#if outputViewMenuOpen}
          <div class="absolute top-full right-0 mt-1 min-w-[10rem] rounded border border-border bg-popover shadow-md overflow-hidden">
            <label class="flex items-center gap-2 px-3 py-1.5 hover:bg-muted cursor-pointer">
              <input type="radio" name="output-view-style" bind:group={outputViewStyle} value="solid" />
              <span>Solid</span>
            </label>
            <label class="flex items-center gap-2 px-3 py-1.5 hover:bg-muted cursor-pointer">
              <input type="radio" name="output-view-style" bind:group={outputViewStyle} value="hidden-line" />
              <span>Hidden line</span>
            </label>
          </div>
        {/if}
      </div>
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
    <Dialog.Footer>
      <Button variant="outline" size="sm" onclick={handleExportCollection}>Export...</Button>
      {#if collectionStore.isEditable}
        <Button variant="destructive" size="sm" class="text-foreground" onclick={() => { deleteCollectionDialogOpen = true; }}>Delete Collection</Button>
      {/if}
    </Dialog.Footer>
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

<DebugCellsDialog bind:open={debugCellsDialogOpen} />
<TriangleInfoDialog bind:open={triangleInfoDialogOpen} pick={pickedTriangle} />
<CellInfoDialog bind:open={cellInfoDialogOpen} info={cellInfo} error={cellInfoError} loading={cellInfoLoading} />

</Tooltip.Provider>
