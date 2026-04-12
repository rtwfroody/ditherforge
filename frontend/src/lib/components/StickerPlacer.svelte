<script lang="ts">
  // Lives inside a threlte <Canvas>. When active, clicking on the mesh
  // raycasts and returns the world-space point, face normal, and camera up.
  import { useThrelte } from '@threlte/core';
  import * as THREE from 'three';
  import { WebGLRenderer } from 'three';

  let {
    active = false,
    onPlace,
  }: {
    active?: boolean;
    onPlace?: (point: [number, number, number], normal: [number, number, number], cameraUp: [number, number, number]) => void;
  } = $props();

  const { renderer, camera, scene } = useThrelte();

  function handleClick(event: MouseEvent) {
    if (!active || !onPlace) return;
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
    if (!(hit.object instanceof THREE.Mesh) || !hit.face) return;

    // World-space point.
    const p = hit.point;

    // Face normal in world space.
    const normalMatrix = new THREE.Matrix3().getNormalMatrix(hit.object.matrixWorld);
    const worldNormal = hit.face.normal.clone().applyMatrix3(normalMatrix).normalize();

    // Camera up vector.
    const camUp = cam.up.clone().normalize();

    onPlace(
      [p.x, p.y, p.z],
      [worldNormal.x, worldNormal.y, worldNormal.z],
      [camUp.x, camUp.y, camUp.z],
    );
  }

  $effect(() => {
    if (!active || !(renderer instanceof WebGLRenderer)) return;
    const canvas = renderer.domElement;
    canvas.addEventListener('click', handleClick);
    canvas.style.cursor = 'crosshair';
    return () => {
      canvas.removeEventListener('click', handleClick);
      canvas.style.cursor = '';
    };
  });
</script>
