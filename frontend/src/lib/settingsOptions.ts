// Single source of truth for the values + labels of every dropdown /
// radio-backed setting. The same constants are consumed by:
//
//   - App.svelte's applySettings(), to validate JSON-loaded values
//     (anything outside the allowed set falls back to the default).
//   - The dropdown markup ({#each} over OPTIONS) so the option list
//     and the validator can never drift.
//   - Custom radio groups (StickerPanel mode, sizeMode, baseColorMode),
//     which import only the TYPE alias to keep the type literal in
//     sync without taking on the {#each} machinery. Those radio
//     blocks DO still hold the literal value strings inline (e.g.
//     `value="size"`) because each option carries its own HelpTip
//     content; renaming a value here would surface as a TypeScript
//     error at the inline `=== 'size'` comparisons in App.svelte.
//
// Default values live in the $state initializers in App.svelte —
// FACTORY_DEFAULTS captures them at init and applySettings hands the
// captured value as the fallback to the validator.

export const SPLIT_ORIENTATION_OPTIONS = [
  { value: 'z-up',   label: '+Z up' },
  { value: 'z-down', label: '−Z up' },
  { value: 'x-up',   label: '+X up' },
  { value: 'x-down', label: '−X up' },
  { value: 'y-up',   label: '+Y up' },
  { value: 'y-down', label: '−Y up' },
] as const;
export type SplitOrientation = typeof SPLIT_ORIENTATION_OPTIONS[number]['value'];

// The two 'pegs*' labels here are placeholders; SplitControls.svelte
// rewrites them per cut axis (e.g. "Pegs on left half" / "Pegs on right
// half") since which side a peg lands on is a world-axis direction.
export const SPLIT_CONNECTOR_OPTIONS = [
  { value: 'none',      label: 'None'                },
  { value: 'pegs',      label: 'Pegs on low side'    },
  { value: 'pegs-high', label: 'Pegs on high side'   },
  { value: 'dowels',    label: 'Dowel/magnet holes'  },
] as const;
export type SplitConnectorStyle = typeof SPLIT_CONNECTOR_OPTIONS[number]['value'];

export const SPLIT_AXIS_OPTIONS = [
  { value: 0, label: 'X' },
  { value: 1, label: 'Y' },
  { value: 2, label: 'Z' },
] as const;
export type SplitAxis = typeof SPLIT_AXIS_OPTIONS[number]['value'];

export const DITHER_OPTIONS = [
  { value: 'riemersma',       label: 'Riemersma'       },
  { value: 'blue-noise',      label: 'Blue noise'      },
  { value: 'dizzy-corrected', label: 'Dizzy'           },
  { value: 'dizzy-local-corrected', label: 'Dizzy local' },
  { value: 'floyd-steinberg', label: 'Floyd-Steinberg' },
  { value: 'none',            label: 'none'            },
] as const;
// Removed/legacy dither strings (e.g. riemersma-pair, dizzy-2hop,
// dizzy-recover) are migrated to a surviving mode by the backend
// settings loader (internal/settings.Load), so the frontend never
// sees them.
export type DitherMode = typeof DITHER_OPTIONS[number]['value'];

export const SIZE_MODE_OPTIONS = [
  { value: 'size',  label: 'Size'  },
  { value: 'scale', label: 'Scale' },
] as const;
export type SizeMode = typeof SIZE_MODE_OPTIONS[number]['value'];

export const BASE_COLOR_MODE_OPTIONS = [
  { value: 'solid',   label: 'Solid'   },
  { value: 'texture', label: 'Texture' },
] as const;
export type BaseColorMode = typeof BASE_COLOR_MODE_OPTIONS[number]['value'];

export const STICKER_MODE_OPTIONS = [
  { value: 'unfold',     label: 'Unfold'     },
  { value: 'projection', label: 'Projection' },
] as const;
export type StickerMode = typeof STICKER_MODE_OPTIONS[number]['value'];

// Convenience: the bare value tuples, used by validators that just
// need an "is this string in the set" check.
export const SPLIT_ORIENTATION_VALUES = SPLIT_ORIENTATION_OPTIONS.map(o => o.value);
export const SPLIT_CONNECTOR_VALUES = SPLIT_CONNECTOR_OPTIONS.map(o => o.value);
export const SPLIT_AXIS_VALUES = SPLIT_AXIS_OPTIONS.map(o => o.value);
export const DITHER_VALUES = DITHER_OPTIONS.map(o => o.value);
export const SIZE_MODE_VALUES = SIZE_MODE_OPTIONS.map(o => o.value);
export const BASE_COLOR_MODE_VALUES = BASE_COLOR_MODE_OPTIONS.map(o => o.value);
export const STICKER_MODE_VALUES = STICKER_MODE_OPTIONS.map(o => o.value);
