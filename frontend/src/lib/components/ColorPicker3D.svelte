<script lang="ts">
  // Lives inside a threlte <Canvas>. When pickMode is true, clicking on the
  // canvas samples the rendered pixel color and calls onPick with the hex value.
  import { useThrelte } from '@threlte/core';
  import { WebGLRenderer } from 'three';

  let {
    pickMode = false,
    onPick,
  }: {
    pickMode?: boolean;
    onPick?: (hex: string) => void;
  } = $props();

  const { renderer } = useThrelte();

  function handleClick(event: MouseEvent) {
    if (!pickMode || !onPick) return;
    if (!(renderer instanceof WebGLRenderer)) return;

    const canvas = renderer.domElement;
    const rect = canvas.getBoundingClientRect();
    const dpr = renderer.getPixelRatio();

    // Convert click position to pixel coordinates in the framebuffer.
    const x = Math.floor((event.clientX - rect.left) * dpr);
    const y = canvas.height - Math.floor((event.clientY - rect.top) * dpr) - 1;

    const gl = renderer.getContext();
    const pixel = new Uint8Array(4);
    gl.readPixels(x, y, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, pixel);

    const hex = `#${pixel[0].toString(16).padStart(2, '0')}${pixel[1].toString(16).padStart(2, '0')}${pixel[2].toString(16).padStart(2, '0')}`.toUpperCase();
    onPick(hex);
  }

  // Attach click listener to the canvas element.
  $effect(() => {
    if (!pickMode || !(renderer instanceof WebGLRenderer)) return;
    const canvas = renderer.domElement;
    canvas.addEventListener('click', handleClick);
    canvas.style.cursor = 'crosshair';
    return () => {
      canvas.removeEventListener('click', handleClick);
      canvas.style.cursor = '';
    };
  });
</script>
