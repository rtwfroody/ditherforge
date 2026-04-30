<script lang="ts">
  import HelpTip from './HelpTip.svelte';
  import { Checkbox } from '$lib/components/ui/checkbox';

  type Props = {
    enabled: boolean;
    axis: number; // 0=X, 1=Y, 2=Z
    offset: number;
    connectorStyle: string; // "none" | "pegs" | "dowels"
    connectorCount: number; // 0=auto, 1..3 explicit
    connectorDiamMM: number;
    connectorDepthMM: number;
    clearanceMM: number;
    gapMM: number;
    // Range hint for the offset slider; populated by App.svelte from
    // the current model's bbox along the selected axis.
    minOffset: number;
    maxOffset: number;
    onAlphaWrapForced?: () => void; // called when toggling Split on
  };

  let {
    enabled = $bindable(),
    axis = $bindable(),
    offset = $bindable(),
    connectorStyle = $bindable(),
    connectorCount = $bindable(),
    connectorDiamMM = $bindable(),
    connectorDepthMM = $bindable(),
    clearanceMM = $bindable(),
    gapMM = $bindable(),
    minOffset,
    maxOffset,
    onAlphaWrapForced,
  }: Props = $props();

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
      or so you can paint each half before assembly. Alignment pegs
      help the halves register when glued. Forces alpha-wrap on
      because the cut needs a watertight input.
    </HelpTip>
  </label>

  {#if enabled}
    <div class="grid grid-cols-2 gap-3 pl-6 text-sm">
      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground">Cut axis</span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={axis}
        >
          <option value={0}>X</option>
          <option value={1}>Y</option>
          <option value={2}>Z</option>
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
          bind:value={offset}
        />
      </label>

      <label class="col-span-2 flex flex-col gap-1">
        <span class="text-muted-foreground">Offset slider</span>
        <input
          type="range"
          step="0.5"
          min={minOffset}
          max={maxOffset}
          bind:value={offset}
        />
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Connector style
          <HelpTip>
            <strong>Pegs</strong>: solid peg on one half, matching pocket
            on the other. <strong>Dowels</strong>: matching pockets on
            both halves; print or buy separate dowel pins.
            <strong>None</strong>: flat cut, glue-only assembly.
          </HelpTip>
        </span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={connectorStyle}
        >
          <option value="none">None</option>
          <option value="pegs">Pegs</option>
          <option value="dowels">Dowel holes</option>
        </select>
      </label>

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Count
          <HelpTip>
            Number of connectors along the cut. Auto picks 1, 2, or 3
            based on the cut polygon's inscribed-circle radius.
          </HelpTip>
        </span>
        <select
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={connectorCount}
          disabled={connectorStyle === 'none'}
        >
          <option value={0}>Auto</option>
          <option value={1}>1</option>
          <option value={2}>2</option>
          <option value={3}>3</option>
        </select>
      </label>

      {#if connectorStyle !== 'none'}
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

      <label class="flex flex-col gap-1">
        <span class="text-muted-foreground flex items-center gap-1.5">
          Bed gap (mm)
          <HelpTip>
            Space between the two halves when laid out side-by-side
            on the print bed.
          </HelpTip>
        </span>
        <input
          type="number"
          step="1"
          min="0"
          class="h-9 rounded border bg-background text-foreground px-2"
          bind:value={gapMM}
        />
      </label>
    </div>
  {/if}
</div>
