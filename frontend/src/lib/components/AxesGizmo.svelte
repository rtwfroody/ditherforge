<script lang="ts">
  import { onDestroy } from 'svelte';
  import { useThrelte, useTask } from '@threlte/core';
  import * as THREE from 'three';

  let { size = 80 }: { size?: number } = $props();

  const { renderer, camera, scheduler, renderStage, shouldRender } = useThrelte();

  // Create a post-render stage that runs after the main render stage.
  const stageKey = Symbol('axes-gizmo');
  const postRenderStage = scheduler.createStage(stageKey, {
    after: renderStage,
  });
  onDestroy(() => scheduler.removeStage(stageKey));

  // Build a tiny scene with colored axes.
  const gizmoScene = new THREE.Scene();
  const gizmoCamera = new THREE.PerspectiveCamera(50, 1, 0.1, 10);
  gizmoCamera.up.set(0, 0, 1); // Match the main scene's Z-up orientation.

  const axisLength = 0.8;
  const axes = new THREE.Group();

  function makeAxis(dir: THREE.Vector3, color: number) {
    const mat = new THREE.LineBasicMaterial({ color, linewidth: 2 });
    const geo = new THREE.BufferGeometry().setFromPoints([
      new THREE.Vector3(0, 0, 0),
      dir.clone().multiplyScalar(axisLength),
    ]);
    const line = new THREE.Line(geo, mat);

    const coneMat = new THREE.MeshBasicMaterial({ color });
    const coneGeo = new THREE.ConeGeometry(0.06, 0.18, 8);
    const cone = new THREE.Mesh(coneGeo, coneMat);
    cone.position.copy(dir.clone().multiplyScalar(axisLength));
    cone.quaternion.setFromUnitVectors(new THREE.Vector3(0, 1, 0), dir);
    axes.add(line, cone);
  }

  makeAxis(new THREE.Vector3(1, 0, 0), 0xff4444); // X = red
  makeAxis(new THREE.Vector3(0, 1, 0), 0x44ff44); // Y = green
  makeAxis(new THREE.Vector3(0, 0, 1), 0x4488ff); // Z = blue

  function makeLabel(text: string, pos: THREE.Vector3, color: string) {
    const canvas = document.createElement('canvas');
    canvas.width = 64;
    canvas.height = 64;
    const ctx = canvas.getContext('2d')!;
    ctx.font = 'bold 48px sans-serif';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillStyle = color;
    ctx.fillText(text, 32, 32);
    const tex = new THREE.CanvasTexture(canvas);
    const mat = new THREE.SpriteMaterial({ map: tex, depthTest: false });
    const sprite = new THREE.Sprite(mat);
    sprite.position.copy(pos);
    sprite.scale.set(0.3, 0.3, 1);
    axes.add(sprite);
  }

  makeLabel('X', new THREE.Vector3(axisLength + 0.2, 0, 0), '#ff4444');
  makeLabel('Y', new THREE.Vector3(0, axisLength + 0.2, 0), '#44ff44');
  makeLabel('Z', new THREE.Vector3(0, 0, axisLength + 0.2), '#4488ff');

  gizmoScene.add(axes);

  // Reusable vector to avoid per-frame allocation.
  const tmpDir = new THREE.Vector3();
  const tmpSize = new THREE.Vector2();

  // Render the gizmo after the main scene in a small viewport.
  // Only render when the main scene also rendered (on-demand mode).
  useTask(() => {
    if (!shouldRender()) return;
    const gl = renderer;
    const cam = camera.current;
    if (!gl || !cam) return;

    // Match the gizmo camera's rotation to the main camera.
    cam.getWorldDirection(tmpDir);
    gizmoCamera.position.copy(tmpDir.multiplyScalar(-2.5));
    gizmoCamera.lookAt(0, 0, 0);

    // Render into bottom-left corner without clearing the color buffer.
    // Three.js's setViewport/setScissor take CSS pixels and multiply by
    // pixelRatio internally. Don't pre-multiply here, or on HiDPI displays
    // the restored viewport ends up pixelRatio^2 too large and the main
    // scene is drawn with NDC origin shifted off to the right.
    const oldAutoClear = gl.autoClear;
    gl.autoClear = false;
    gl.setViewport(0, 0, size, size);
    gl.setScissor(0, 0, size, size);
    gl.setScissorTest(true);
    try {
      gl.clearDepth();
      gl.render(gizmoScene, gizmoCamera);
    } finally {
      // Restore full viewport using the renderer's CSS size (NOT
      // domElement.width/height, which are drawing-buffer pixels).
      gl.setScissorTest(false);
      gl.getSize(tmpSize);
      gl.setViewport(0, 0, tmpSize.x, tmpSize.y);
      gl.autoClear = oldAutoClear;
    }
  }, { autoInvalidate: false, stage: postRenderStage });
</script>
