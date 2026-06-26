<script lang="ts">
  import HelpTip from './HelpTip.svelte';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import {
    SPLIT_ORIENTATION_OPTIONS,
    SPLIT_CONNECTOR_OPTIONS,
    type SplitOrientation,
    type SplitConnectorStyle,
    type SplitAxis,
  } from '$lib/settingsOptions';

  type Props = {
    enabled: boolean;
    axis: SplitAxis;
    offset: number;
    // Cut-plane tilt in degrees about its two in-plane axes. 0/0 keeps
    // the plane perpendicular to the chosen axis (legacy behaviour).
    tiltA: number;
    tiltB: number;
    connectorStyle: SplitConnectorStyle;
    connectorCount: number; // 0=auto, 1..3 explicit
    connectorDiamMM: number;
    connectorDepthMM: number;
    clearanceMM: number;
    // orientationA is half 0 (low-axis side), orientationB is half 1
    // (high-axis side). See settingsOptions.ts for the value set.
    orientationA: SplitOrientation;
    orientationB: SplitOrientation;
    // Range hint for the offset slider, in mm; populated by App.svelte from
    // the current model's bbox along the selected axis.
    minOffset: number;
    maxOffset: number;
    // Model max extent at print size, in mm. offset is stored as a fraction
    // of this; the UI multiplies by it to show/edit mm. 0 when unknown.
    extentMM: number;
    onAlphaWrapForced?: () => void; // called when toggling Split on
  };

  let {
    enabled = $bindable(),
    axis = $bindable(),
    offset = $bindable(),
    tiltA = $bindable(),
    tiltB = $bindable(),
    connectorStyle = $bindable(),
    connectorCount = $bindable(),
    connectorDiamMM = $bindable(),
    connectorDepthMM = $bindable(),
    clearanceMM = $bindable(),
    orientationA = $bindable(),
    orientationB = $bindable(),
    extentMM,
    minOffset,
    maxOffset,
    onAlphaWrapForced,
  }: Props = $props();

  // offset is a fraction of the print extent; the inputs below work in mm.
  const offsetMM = $derived(extentMM > 0 ? offset * extentMM : 0);
  function setOffsetMM(mm: number) {
    if (extentMM > 0 && isFinite(mm)) offset = mm / extentMM;
  }

  // Toggling Split on cascades to enabling AlphaWrap (the cut needs a
  // watertight input). The parent listens via onAlphaWrapForced and
  // sets its alphaWrap state. The reverse cascade (turning AlphaWrap
  // off auto-disables Split) lives in App.svelte where alphaWrap is
  // owned.
  function handleEnabledChange(v: boolean) {
    enabled = v;
    if (v && onAlphaWrapForced) {
      onAlphaWrapForced();
    }
  }

  const axisLabel = $derived(['X', 'Y', 'Z'][axis] ?? 'Z');

  // The two in-plane axes the tilt angles rotate the cut plane about,
  // keyed off the cut axis. Mirrors the AxisBasis convention: for cut
  // axis Z the in-plane basis is (U=+X, V=+Y), so tiltA rotates about X
  // and tiltB about Y, and likewise for the other axes.
  const tiltAxes = $derived(
    axis === 0
      ? { a: 'Y', b: 'Z' }
      : axis === 1
        ? { a: 'Z', b: 'X' }
        : { a: 'X', b: 'Y' }
  );

  // Half labels mirror the side-by-side layout: half 0 is the lower-X
  // half on the bed, half 1 is the higher-X half. The pair of labels
  // depends on the cut axis so the user can map the panel back to
  // their model.
  const halfLabels = $derived(
    axis === 0
      ? { a: 'Left half (−X)', b: 'Right half (+X)' }
      : axis === 1
        ? { a: 'Front half (−Y)', b: 'Back half (+Y)' }
        : { a: 'Bottom half (−Z)', b: 'Top half (+Z)' }
  );

  // Short side names for the two peg options, keyed off the cut axis.
  // 'pegs' puts the male peg on the low-coordinate half, 'pegs-high' on
  // the high-coordinate half.
  const pegSides = $derived(
    axis === 0
      ? { low: 'left (−X)', high: 'right (+X)' }
      : axis === 1
        ? { low: 'front (−Y)', high: 'back (+Y)' }
        : { low: 'bottom (−Z)', high: 'top (+Z)' }
  );

  // Orientation options, relabeled per cut axis. The two options whose
  // axis equals the cut axis seat the half on its cut/seam face, so they
  // are shown as "Cut face up/down" (and the backend's CapAlign rotation
  // seats that face flat even when the plane is tilted). The other four
  // align to a model side and keep their +/−axis labels.
  //
  // The two halves carry opposite cut-face normals, so the same enum
  // value seats half 0's cap up but half 1's cap down. We therefore
  // mirror the up/down label between halves so "Cut face down" always
  // means "this half's seam on the bed". Values are unchanged.
  const ORIENT_AXIS_LETTER = ['x', 'y', 'z'];
  function orientationOptionsFor(half: number) {
    const a = ORIENT_AXIS_LETTER[axis] ?? 'z';
    return SPLIT_ORIENTATION_OPTIONS.map((opt) => {
      if (opt.value === `${a}-up`) {
        return { value: opt.value, label: half === 0 ? 'Cut face up' : 'Cut face down' };
      }
      if (opt.value === `${a}-down`) {
        return { value: opt.value, label: half === 0 ? 'Cut face down' : 'Cut face up' };
      }
      return { value: opt.value, label: opt.label };
    });
  }
  const orientationOptionsA = $derived(orientationOptionsFor(0));
  const orientationOptionsB = $derived(orientationOptionsFor(1));

  // Connector options with axis-aware labels for the two peg variants.
  const connectorOptions = $derived(
    SPLIT_CONNECTOR_OPTIONS.map((opt) =>
      opt.value === 'pegs'
        ? { value: opt.value, label: `Pegs on ${pegSides.low} half` }
        : opt.value === 'pegs-high'
          ? { value: opt.value, label: `Pegs on ${pegSides.high} half` }
          : { value: opt.value, label: opt.label }
    )
  );
</script>

<div class="space-y-3">
  <label class="flex items-center gap-2 text-sm font-medium">
    <Checkbox
      checked={enabled}
      onCheckedChange={(v: boolean) => handleEnabledChange(!!v)}
    />
    Split into two parts
    <HelpTip>
      Cut the model in two pieces so each half fits the build volume,
      or so supports that would otherwise be hard to remove become
      easy to access. Alignment pegs help the halves register when
      glued. Forces alpha-wrap on because the cut needs a watertight
      input.
    </HelpTip>
  </label>

  {#if enabled}
    <div class="grid grid-cols-2 gap-3 pl-6 text-sm">
      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground">Cut plane</span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={axis}
        >
          <option value={0}>YZ</option>
          <option value={1}>XZ</option>
          <option value={2}>XY</option>
        </select>
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Offset (mm)
          <HelpTip>
            Position of the cut plane along the {axisLabel} axis,
            measured from the model's local origin.
          </HelpTip>
        </span>
        <input
          type="number"
          step="0.1"
          min={minOffset}
          max={maxOffset}
          class="h-9 rounded border bg-background text-foreground px-2"
          value={offsetMM}
          oninput={(e) => setOffsetMM(parseFloat(e.currentTarget.value))}
        />
      </label>

      <label class="col-span-2 flex flex-col gap-1">
        <span class="text-muted-foreground">Offset slider</span>
        <input
          type="range"
          step="0.5"
          min={minOffset}
          max={maxOffset}
          value={offsetMM}
          oninput={(e) => setOffsetMM(parseFloat(e.currentTarget.value))}
        />
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Tilt about {tiltAxes.a} (°)
          <HelpTip>
            Rotate the cut plane off the {axisLabel} axis. 0° keeps the
            cut perpendicular to {axisLabel}. This angle tilts the plane
            about the in-plane {tiltAxes.a} direction; combine with the
            other tilt for a fully oblique cut.
          </HelpTip>
        </span>
        <input
          type="number"
          step="1"
          min="-85"
          max="85"
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={tiltA}
        />
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Tilt about {tiltAxes.b} (°)
          <HelpTip>
            Rotate the cut plane off the {axisLabel} axis about the
            in-plane {tiltAxes.b} direction. 0° plus the other tilt at 0°
            gives a flat axis-aligned cut.
          </HelpTip>
        </span>
        <input
          type="number"
          step="1"
          min="-85"
          max="85"
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={tiltB}
        />
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Connector style
          <HelpTip>
            <strong>Pegs</strong>: solid peg on one half, matching pocket
            on the other. The two peg options choose which half carries
            the male pegs (the label names the side for the current cut
            axis). <strong>Dowel/magnet holes</strong>: matching pockets
            on both halves — print or buy dowel pins, or glue in magnets
            for a magnetic catch. <strong>None</strong>: flat cut,
            glue-only assembly.
          </HelpTip>
        </span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={connectorStyle}
        >
          {#each connectorOptions as opt}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </label>

      {#if connectorStyle !== 'none'}
        <label class="flex flex-col gap-1">
          <span class="text-muted-foreground flex items-center gap-1.5">
            Count
            <HelpTip>
              Number of connectors along the cut. Auto picks 1, 2, or
              3 based on the cut polygon's inscribed-circle radius.
            </HelpTip>
          </span>
          <select
            class="h-9 rounded border bg-background text-foreground px-2"
            bind:value={connectorCount}
          >
            <option value={0}>Auto</option>
            <option value={1}>1</option>
            <option value={2}>2</option>
            <option value={3}>3</option>
          </select>
        </label>

        <label class="flex flex-col gap-1">
          <span class="text-muted-foreground">Diameter (mm)</span>
          <input
            type="number"
            step="0.5"
            min="1"
            class="h-9 rounded border bg-background text-foreground px-2"
            bind:value={connectorDiamMM}
          />
        </label>

        <label class="flex flex-col gap-1">
          <span class="text-muted-foreground">Depth (mm)</span>
          <input
            type="number"
            step="0.5"
            min="1"
            class="h-9 rounded border bg-background text-foreground px-2"
            bind:value={connectorDepthMM}
          />
        </label>

        <label class="flex flex-col gap-1">
          <span class="text-muted-foreground flex items-center gap-1.5">
            Clearance (mm)
            <HelpTip>
              Per-side radial clearance applied to the female feature
              so the peg slides in. 0.15 mm works for most FDM prints.
            </HelpTip>
          </span>
          <input
            type="number"
            step="0.05"
            min="0"
            class="h-9 rounded border bg-background text-foreground px-2"
            bind:value={clearanceMM}
          />
        </label>
      {/if}

      <label class="col-start-1 flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          {halfLabels.a}
          <HelpTip>
            How this half sits on the bed. <strong>Cut face down/up</strong>
            rests the half on its seam (the cut surface) — the usual
            choice for gluing, and it stays flat even when the cut plane
            is tilted. The <strong>±axis up</strong> options instead rest
            the half on a model side, unaffected by any cut tilt. The
            remaining spin about the vertical is fixed automatically to
            stay close to the authored orientation.
          </HelpTip>
        </span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={orientationA}
        >
          {#each orientationOptionsA as opt}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground">{halfLabels.b}</span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={orientationB}
        >
          {#each orientationOptionsB as opt}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </label>

    </div>
  {/if}
</div>
