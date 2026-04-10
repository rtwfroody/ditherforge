<script lang="ts">
  // Triggers a Threlte re-render whenever any prop changes.
  // Must be a child of <Canvas>.
  import { useThrelte } from '@threlte/core';

  let { brightness = 0, contrast = 0, saturation = 0, extra = '' }: {
    brightness?: number;
    contrast?: number;
    saturation?: number;
    extra?: string;
  } = $props();

  const { invalidate } = useThrelte();

  $effect(() => {
    void brightness;
    void contrast;
    void saturation;
    void extra;
    invalidate();
  });
</script>
