<script lang="ts">
  // Lives inside a threlte <Canvas>. When pickMode is true, clicking on the
  // canvas raycasts into the scene to sample the vertex color (pre-lighting)
  // so that picked colors match what the warp shader sees.
  import { useThrelte } from '@threlte/core';
  import * as THREE from 'three';
  import { WebGLRenderer } from 'three';

  let {
    pickMode = false,
    onPick,
    brightness = 0,
    contrast = 0,
    saturation = 0,
  }: {
    pickMode?: boolean;
    onPick?: (hex: string) => void;
    brightness?: number;
    contrast?: number;
    saturation?: number;
  } = $props();

  const { renderer, camera, scene } = useThrelte();

  // Convert linear [0,1] to sRGB [0,1].
  function linearToSRGB(c: number): number {
    return c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055;
  }

  // Apply brightness/contrast/saturation adjustment to sRGB [0,1] values.
  // Must match Go's AdjustColor in internal/voxel/coloradjust.go.
  function adjustColor(r: number, g: number, b: number): [number, number, number] {
    const br = brightness / 100;
    const co = (100 + contrast) / 100;
    const sa = (100 + saturation) / 100;
    r += br; g += br; b += br;
    r = (r - 0.5) * co + 0.5;
    g = (g - 0.5) * co + 0.5;
    b = (b - 0.5) * co + 0.5;
    const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b;
    r = lum + sa * (r - lum);
    g = lum + sa * (g - lum);
    b = lum + sa * (b - lum);
    return [Math.max(0, Math.min(1, r)), Math.max(0, Math.min(1, g)), Math.max(0, Math.min(1, b))];
  }

  function handleClick(event: MouseEvent) {
    if (!pickMode || !onPick) return;
    if (!(renderer instanceof WebGLRenderer)) return;

    const canvas = renderer.domElement;
    const rect = canvas.getBoundingClientRect();

    // Normalized device coordinates [-1, 1].
    const ndc = new THREE.Vector2(
      ((event.clientX - rect.left) / rect.width) * 2 - 1,
      -((event.clientY - rect.top) / rect.height) * 2 + 1,
    );

    const cam = camera.current;
    // Ensure camera matrices are up-to-date (they may be stale between renders).
    cam.updateMatrixWorld();
    const raycaster = new THREE.Raycaster();
    raycaster.setFromCamera(ndc, cam);
    const hits = raycaster.intersectObjects(scene.children, true);
    if (hits.length === 0) return;

    const hit = hits[0];
    const mesh = hit.object;
    if (!(mesh instanceof THREE.Mesh) || !hit.face) return;

    const geometry = mesh.geometry as THREE.BufferGeometry;
    const colorAttr = geometry.getAttribute('color') as THREE.BufferAttribute | null;

    if (colorAttr) {
      // Vertex-colored mesh: read the face's vertex color (linear RGB).
      // For flat-shaded face colors, all 3 vertices share the same color.
      const idx = hit.face.a;
      const lr = colorAttr.getX(idx);
      const lg = colorAttr.getY(idx);
      const lb = colorAttr.getZ(idx);

      // Convert linear → sRGB, then apply B/C/S adjustment (matching Go pipeline
      // order: vertex colors are sRGB, adjustment happens on sRGB values).
      const [ar, ag, ab] = adjustColor(linearToSRGB(lr), linearToSRGB(lg), linearToSRGB(lb));
      const r = Math.round(ar * 255);
      const g = Math.round(ag * 255);
      const b = Math.round(ab * 255);
      const hex = `#${r.toString(16).padStart(2, '0')}${g.toString(16).padStart(2, '0')}${b.toString(16).padStart(2, '0')}`.toUpperCase();
      onPick(hex);
    } else if (hit.uv) {
      // Textured mesh: sample the texture at the hit UV.
      const material = mesh.material as THREE.MeshStandardMaterial;
      if (material.map) {
        const img = material.map.image as HTMLImageElement;
        const canvas2d = document.createElement('canvas');
        canvas2d.width = img.naturalWidth;
        canvas2d.height = img.naturalHeight;
        const ctx = canvas2d.getContext('2d')!;
        ctx.drawImage(img, 0, 0);
        const u = ((hit.uv.x % 1) + 1) % 1;
        const v = ((hit.uv.y % 1) + 1) % 1;
        const px = Math.floor(u * img.naturalWidth);
        // Textures are loaded with flipY=false, so UV v maps directly to image y.
        const py = Math.floor(v * img.naturalHeight);
        const pixel = ctx.getImageData(px, py, 1, 1).data;
        // Texture pixels are already sRGB. Apply B/C/S adjustment.
        const [ar, ag, ab] = adjustColor(pixel[0] / 255, pixel[1] / 255, pixel[2] / 255);
        const hex = `#${Math.round(ar * 255).toString(16).padStart(2, '0')}${Math.round(ag * 255).toString(16).padStart(2, '0')}${Math.round(ab * 255).toString(16).padStart(2, '0')}`.toUpperCase();
        onPick(hex);
      }
    }
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
