<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Label } from '$lib/components/ui/label';
  import { Slider } from '$lib/components/ui/slider';
  import HelpTip from '$lib/components/HelpTip.svelte';
  import { ImageIcon, TrashIcon, CrosshairIcon } from '@lucide/svelte';
  import type { StickerMode } from '$lib/settingsOptions';

  export type StickerUI = {
    imagePath: string;
    fileName: string;
    thumbnail: string;
    center: [number, number, number] | null;
    normal: [number, number, number] | null;
    up: [number, number, number] | null;
    scale: number;
    rotation: number;
    maxAngle: number;
    mode: StickerMode;
  };

  let {
    stickers = $bindable([]),
    placingIndex = $bindable(-1),
    onAdd,
    onRemove,
  }: {
    stickers: StickerUI[];
    placingIndex: number;
    onAdd: () => void;
    onRemove: (index: number) => void;
  } = $props();
</script>

<div class="space-y-3">
  <div class="flex items-center justify-between">
    <div class="flex items-center gap-1.5">
      <Label>Stickers</Label>
      <HelpTip>
        Overlay images onto the model surface. Add an image, then click on the model to position it. Stickers are baked into the color output.
      </HelpTip>
    </div>
    <Button variant="outline" size="sm" onclick={onAdd}>
      <ImageIcon class="w-3.5 h-3.5 mr-1" />Add
    </Button>
  </div>

  {#each stickers as sticker, i}
    <div class="border rounded-md p-2 space-y-2">
      <div class="flex gap-2">
        {#if sticker.thumbnail}
          <!-- Wrapper holds the border + clips the rotated image at its edges;
               CSS rotate() is CW, matching the sticker's on-surface rotation. -->
          <div class="w-12 h-12 rounded border shrink-0 overflow-hidden bg-muted/20" title={sticker.imagePath}>
            <img src={sticker.thumbnail} alt={sticker.fileName}
              class="w-full h-full object-contain"
              style="transform: rotate({sticker.rotation}deg)" />
          </div>
        {/if}
        <div class="flex-1 min-w-0 space-y-0.5">
          <div class="flex items-center justify-between gap-1">
            <span class="text-xs text-muted-foreground truncate" title={sticker.imagePath}>
              {sticker.fileName}
            </span>
            <div class="flex items-center gap-0.5 shrink-0">
              <Button
                variant={placingIndex === i ? "default" : "outline"}
                size="sm"
                class="h-6 px-2 text-xs"
                onclick={() => placingIndex = placingIndex === i ? -1 : i}
              >
                <CrosshairIcon class="w-3 h-3 mr-1" />
                {placingIndex === i ? 'Placing...' : sticker.center ? 'Reposition' : 'Place'}
              </Button>
              <Button variant="ghost" size="sm" class="h-6 w-6 p-0" onclick={() => onRemove(i)}>
                <TrashIcon class="w-3.5 h-3.5" />
              </Button>
            </div>
          </div>
          {#if sticker.center}
            <div class="text-[10px] text-muted-foreground">
              Placed at ({sticker.center[0].toFixed(1)}, {sticker.center[1].toFixed(1)}, {sticker.center[2].toFixed(1)})
            </div>
          {:else}
            <div class="text-[10px] text-muted-foreground italic">Click the model to place</div>
          {/if}
        </div>
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between">
          <span class="text-xs flex items-center gap-1.5">
            Scale
            <HelpTip>
              Size of the sticker on the model, in millimeters along its longest dimension.
            </HelpTip>
          </span>
          <span class="text-[10px] text-muted-foreground w-12 text-right">{sticker.scale.toFixed(1)} mm</span>
        </div>
        <Slider type="single" min={1} max={200} step={1} value={sticker.scale}
          onValueChange={(v: number) => { stickers[i] = { ...sticker, scale: v }; stickers = stickers; }} />
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between">
          <span class="text-xs flex items-center gap-1.5">
            Rotation
            <HelpTip>
              Rotate the sticker around the surface normal at its placement point.
            </HelpTip>
          </span>
          <span class="text-[10px] text-muted-foreground w-8 text-right">{sticker.rotation}°</span>
        </div>
        <Slider type="single" min={0} max={360} step={1} value={sticker.rotation}
          onValueChange={(v: number) => { stickers[i] = { ...sticker, rotation: v }; stickers = stickers; }} />
      </div>

      <div class="space-y-1">
        <span class="text-xs">Mode</span>
        <div class="flex gap-3 text-xs">
          <label class="flex items-center gap-1">
            <input type="radio" name={"sticker-mode-" + i} value="projection"
              checked={sticker.mode === 'projection'}
              onchange={() => { stickers[i] = { ...sticker, mode: 'projection' }; stickers = stickers; }} />
            Projection
            <HelpTip>
              Stamps the sticker from a single direction, like a slide projector. Works well on most shapes, including complex or non-developable geometry. Can stretch on surfaces that curve away from the projection direction.
            </HelpTip>
          </label>
          <label class="flex items-center gap-1">
            <input type="radio" name={"sticker-mode-" + i} value="unfold"
              checked={sticker.mode === 'unfold'}
              onchange={() => { stickers[i] = { ...sticker, mode: 'unfold' }; stickers = stickers; }} />
            Unfold
            <HelpTip>
              Drapes the sticker over the surface, wrapping around curves. Best on developable patches like cylinders or gentle curves. If it looks wrong on a complex mesh, try enabling Alpha-wrap — it gives unfold a cleaner surface to work from.
            </HelpTip>
          </label>
        </div>
      </div>

      {#if sticker.mode === 'unfold'}
        <div class="space-y-1">
          <div class="flex items-center justify-between">
            <span class="text-xs flex items-center gap-1.5">
              Surface bend limit
              <HelpTip>
                Maximum angle the sticker will wrap around sharp edges before stopping. 0 disables the limit (wraps freely).
              </HelpTip>
            </span>
            <span class="text-[10px] text-muted-foreground w-12 text-right">{sticker.maxAngle === 0 ? 'off' : sticker.maxAngle + '°'}</span>
          </div>
          <Slider type="single" min={0} max={180} step={5} value={sticker.maxAngle}
            onValueChange={(v: number) => { stickers[i] = { ...sticker, maxAngle: v }; stickers = stickers; }} />
        </div>
      {/if}
    </div>
  {/each}
</div>
