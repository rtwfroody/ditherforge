<script lang="ts">
  // Triggers a Threlte re-render whenever any prop changes.
  // Must be a child of <Canvas>.
  import { useThrelte } from '@threlte/core';

  let { brightness = 0, contrast = 0, saturation = 0 }: {
    brightness?: number;
    contrast?: number;
    saturation?: number;
  } = $props();

  const { invalidate } = useThrelte();

  $effect(() => {
    void brightness;
    void contrast;
    void saturation;
    invalidate();
  });
</script>
