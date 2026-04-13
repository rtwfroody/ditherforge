<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Label } from '$lib/components/ui/label';
  import { Slider } from '$lib/components/ui/slider';
  import { ImageIcon, TrashIcon, CrosshairIcon } from '@lucide/svelte';

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
    <Label>Stickers</Label>
    <Button variant="outline" size="sm" onclick={onAdd}>
      <ImageIcon class="w-3.5 h-3.5 mr-1" />Add
    </Button>
  </div>

  {#each stickers as sticker, i}
    <div class="border rounded-md p-2 space-y-2">
      <div class="flex gap-2">
        {#if sticker.thumbnail}
          <img src={sticker.thumbnail} alt={sticker.fileName} title={sticker.imagePath}
            class="w-12 h-12 object-contain rounded border shrink-0" />
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
          <span class="text-xs">Scale</span>
          <span class="text-[10px] text-muted-foreground w-12 text-right">{sticker.scale.toFixed(1)} mm</span>
        </div>
        <Slider type="single" min={1} max={200} step={1} value={sticker.scale}
          onValueChange={(v: number) => { stickers[i] = { ...sticker, scale: v }; stickers = stickers; }} />
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between">
          <span class="text-xs">Rotation</span>
          <span class="text-[10px] text-muted-foreground w-8 text-right">{sticker.rotation}°</span>
        </div>
        <Slider type="single" min={0} max={360} step={1} value={sticker.rotation}
          onValueChange={(v: number) => { stickers[i] = { ...sticker, rotation: v }; stickers = stickers; }} />
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between">
          <span class="text-xs">Surface bend limit</span>
          <span class="text-[10px] text-muted-foreground w-12 text-right">{sticker.maxAngle === 0 ? 'off' : sticker.maxAngle + '°'}</span>
        </div>
        <Slider type="single" min={0} max={180} step={5} value={sticker.maxAngle}
          onValueChange={(v: number) => { stickers[i] = { ...sticker, maxAngle: v }; stickers = stickers; }} />
      </div>
    </div>
  {/each}

  {#if stickers.length === 0}
    <p class="text-xs text-muted-foreground">No stickers added yet.</p>
  {/if}
</div>
