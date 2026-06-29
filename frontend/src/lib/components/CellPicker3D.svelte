<script lang="ts">
  // Lives inside a threlte <Canvas>. When pickMode is true, a tap (a
  // pointerdown→pointerup with negligible movement) raycasts into the
  // scene and reports the picked surface point (world coordinates, i.e.
  // preview-mm) plus the hit face index via onPick. The backend resolves
  // which Voxelize cell sits under that point.
  //
  // We use pointerdown/pointerup with a movement threshold rather than a
  // plain `click` listener: OrbitControls captures the pointer on the
  // canvas, so the synthesized `click` never fires and pointerup never
  // reaches a canvas-bound listener. Recording the down on the canvas and
  // catching the up on window (capture phase) sidesteps both, and the
  // tap-vs-drag threshold lets the user still rotate (drag) while picking.
  import { useThrelte } from '@threlte/core';
  import * as THREE from 'three';
  import { WebGLRenderer } from 'three';

  let {
    pickMode = false,
    onPick,
  }: {
    pickMode?: boolean;
    onPick?: (hit: {
      faceIndex: number;
      point: [number, number, number];
    }) => void;
  } = $props();

  const { renderer, camera, scene } = useThrelte();

  // A pointerup within DRAG_PX of its pointerdown is a pick; more was a
  // rotate/pan drag and is ignored.
  const DRAG_PX = 5;
  let downX = 0;
  let downY = 0;
  let downId = -1;

  function pick(event: PointerEvent) {
    if (!pickMode || !onPick) return;
    if (!(renderer instanceof WebGLRenderer)) return;

    const canvas = renderer.domElement;
    const rect = canvas.getBoundingClientRect();
    const ndc = new THREE.Vector2(
      ((event.clientX - rect.left) / rect.width) * 2 - 1,
      -((event.clientY - rect.top) / rect.height) * 2 + 1,
    );

    const cam = camera.current;
    cam.updateMatrixWorld();
    const raycaster = new THREE.Raycaster();
    raycaster.setFromCamera(ndc, cam);
    const hits = raycaster.intersectObjects(scene.children, true);
    // First mesh hit with a face index (skip gizmos / helpers).
    const hit = hits.find((h) => h.object instanceof THREE.Mesh && h.faceIndex != null);
    if (!hit || hit.faceIndex == null) return;

    onPick({
      faceIndex: hit.faceIndex,
      point: [hit.point.x, hit.point.y, hit.point.z],
    });
  }

  function onPointerDown(event: PointerEvent) {
    if (!pickMode) return;
    downX = event.clientX;
    downY = event.clientY;
    downId = event.pointerId;
  }

  function onPointerUp(event: PointerEvent) {
    if (!pickMode) return;
    if (event.pointerId !== downId) return;
    downId = -1;
    const moved = Math.hypot(event.clientX - downX, event.clientY - downY);
    if (moved <= DRAG_PX) pick(event);
  }

  $effect(() => {
    if (!pickMode || !(renderer instanceof WebGLRenderer)) return;
    const canvas = renderer.domElement;
    canvas.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('pointerup', onPointerUp, true);
    const prevCursor = canvas.style.cursor;
    canvas.style.cursor = 'crosshair';
    return () => {
      canvas.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('pointerup', onPointerUp, true);
      canvas.style.cursor = prevCursor;
    };
  });
</script>
