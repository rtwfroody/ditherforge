<script lang="ts">
  // Lives inside a threlte <Canvas>. When pickMode is true, a single
  // canvas click raycasts into the scene and reports the picked face's
  // three vertex positions (in world coordinates) via onPick.
  //
  // Sibling of ColorPicker3D — same lifecycle, same useThrelte pattern.
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
      vertices: [
        [number, number, number],
        [number, number, number],
        [number, number, number],
      ];
    }) => void;
  } = $props();

  const { renderer, camera, scene } = useThrelte();

  function handleClick(event: MouseEvent) {
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
    if (hits.length === 0) return;

    const hit = hits[0];
    const mesh = hit.object;
    if (!(mesh instanceof THREE.Mesh) || !hit.face || hit.faceIndex == null) return;
    const faceIndex = hit.faceIndex;

    const geometry = mesh.geometry as THREE.BufferGeometry;
    const posAttr = geometry.getAttribute('position') as THREE.BufferAttribute | null;
    if (!posAttr) return;

    mesh.updateMatrixWorld();
    const readVertex = (vi: number): [number, number, number] => {
      const v = new THREE.Vector3(posAttr.getX(vi), posAttr.getY(vi), posAttr.getZ(vi));
      v.applyMatrix4(mesh.matrixWorld);
      return [v.x, v.y, v.z];
    };

    onPick({
      faceIndex,
      vertices: [readVertex(hit.face.a), readVertex(hit.face.b), readVertex(hit.face.c)],
    });
  }

  $effect(() => {
    if (!pickMode || !(renderer instanceof WebGLRenderer)) return;
    const canvas = renderer.domElement;
    canvas.addEventListener('click', handleClick);
    const prevCursor = canvas.style.cursor;
    canvas.style.cursor = 'crosshair';
    return () => {
      canvas.removeEventListener('click', handleClick);
      canvas.style.cursor = prevCursor;
    };
  });
</script>
