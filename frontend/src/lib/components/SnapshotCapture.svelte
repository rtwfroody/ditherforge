<script lang="ts">
  // Hands the parent a function that captures the current WebGL render as a
  // PNG data URL. Must be a child of <Canvas> so useThrelte() resolves the
  // renderer/scene/camera.
  //
  // preserveDrawingBuffer caveat: Threlte's WebGLRenderer is created with the
  // Three.js default (preserveDrawingBuffer: false), so the drawing buffer is
  // cleared after each composite — a toDataURL() on a stale buffer returns
  // blank. We therefore render synchronously and read the buffer back in the
  // same tick, which is the reliable way to capture without forcing
  // preserveDrawingBuffer (which carries a memory/perf cost on every frame).
  import { useThrelte } from '@threlte/core';

  let { onReady }: { onReady?: (capture: (() => string | null) | null) => void } = $props();

  const ctx = useThrelte();

  $effect(() => {
    const capture = (): string | null => {
      const renderer = ctx.renderer;
      const scene = ctx.scene;
      const camera = ctx.camera.current;
      if (!renderer || !scene || !camera) return null;
      try {
        renderer.render(scene, camera);
        return renderer.domElement.toDataURL('image/png');
      } catch {
        return null;
      }
    };
    onReady?.(capture);
    return () => onReady?.(null);
  });
</script>
