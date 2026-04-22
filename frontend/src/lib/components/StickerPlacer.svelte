<script lang="ts">
  // Lives inside a threlte <Canvas>. When active, clicking on the mesh
  // raycasts and returns the world-space point, face normal, and camera up.
  // While moving the mouse, shows a floating billboard preview of the sticker.
  import { useThrelte } from '@threlte/core';
  import * as THREE from 'three';
  import { WebGLRenderer } from 'three';

  let {
    active = false,
    onPlace,
    stickerImage = '',
    stickerSize = 1,
    stickerRotation = 0,
  }: {
    active?: boolean;
    onPlace?: (point: [number, number, number], normal: [number, number, number], cameraUp: [number, number, number]) => void;
    stickerImage?: string;
    stickerSize?: number;
    stickerRotation?: number;
  } = $props();

  const { renderer, camera, scene, invalidate } = useThrelte();

  let billboard: THREE.Mesh | null = null;
  let billboardTexture: THREE.Texture | null = null;
  let currentImageUrl = '';
  let stickerAspect = 1; // imgHeight / imgWidth

  // Scratch buffers reused across mousemove events to avoid per-frame allocations.
  // Reallocated when the placement session starts (see $effect below).
  let ndcScratch = new THREE.Vector2();
  let raycasterScratch = new THREE.Raycaster();
  let normalMatrixScratch = new THREE.Matrix3();
  let nScratch = new THREE.Vector3();
  let uScratch = new THREE.Vector3();
  let tScratch = new THREE.Vector3();
  let bScratch = new THREE.Vector3();
  let crossScratch = new THREE.Vector3();
  let tmpScratch = new THREE.Vector3();
  let basisScratch = new THREE.Matrix4();

  function ensureBillboard() {
    if (!billboard) {
      const geo = new THREE.PlaneGeometry(1, 1);
      const mat = new THREE.MeshBasicMaterial({
        transparent: true,
        opacity: 0.7,
        depthTest: false,
        side: THREE.DoubleSide,
      });
      billboard = new THREE.Mesh(geo, mat);
      billboard.renderOrder = 999;
      scene.add(billboard);
    }
    // Update texture if image changed.
    if (stickerImage && stickerImage !== currentImageUrl) {
      currentImageUrl = stickerImage;
      const loader = new THREE.TextureLoader();
      loader.load(stickerImage, (tex) => {
        // Component may have been torn down before the load finished.
        if (!billboard) {
          tex.dispose();
          return;
        }
        tex.colorSpace = THREE.SRGBColorSpace;
        if (billboardTexture) billboardTexture.dispose();
        billboardTexture = tex;
        const img = tex.image as HTMLImageElement;
        if (img && img.width > 0) {
          stickerAspect = img.height / img.width;
        }
        const mat = billboard.material as THREE.MeshBasicMaterial;
        mat.map = tex;
        mat.needsUpdate = true;
        invalidate();
      });
    }
    return billboard;
  }

  function removeBillboard() {
    if (billboard) {
      scene.remove(billboard);
      (billboard.material as THREE.MeshBasicMaterial).dispose();
      billboard.geometry.dispose();
      billboard = null;
    }
    if (billboardTexture) {
      billboardTexture.dispose();
      billboardTexture = null;
    }
    currentImageUrl = '';
    stickerAspect = 1;
  }

  function raycast(event: MouseEvent) {
    if (!(renderer instanceof WebGLRenderer)) return null;

    const canvas = renderer.domElement;
    const rect = canvas.getBoundingClientRect();
    ndcScratch.set(
      ((event.clientX - rect.left) / rect.width) * 2 - 1,
      -((event.clientY - rect.top) / rect.height) * 2 + 1,
    );

    const cam = camera.current;
    cam.updateMatrixWorld();
    raycasterScratch.setFromCamera(ndcScratch, cam);

    // Exclude the billboard from raycasting.
    const objects = scene.children.filter(c => c !== billboard);
    return raycasterScratch.intersectObjects(objects, true);
  }

  function handleMouseMove(event: MouseEvent) {
    if (!active) return;

    const hits = raycast(event);
    if (!hits || hits.length === 0 || !(hits[0].object instanceof THREE.Mesh) || !hits[0].face) {
      if (billboard) billboard.visible = false;
      invalidate();
      return;
    }

    const hit = hits[0];
    const bb = ensureBillboard();

    // Surface normal in world space.
    normalMatrixScratch.getNormalMatrix(hit.object.matrixWorld);
    nScratch.copy(hit.face!.normal).applyMatrix3(normalMatrixScratch).normalize();

    // Position at hit point, offset slightly along normal to avoid z-fighting.
    bb.position.copy(hit.point).addScaledVector(nScratch, stickerSize * 0.01);

    // Build tangent frame matching BuildStickerDecal in internal/voxel/sticker.go:
    // n = normal, u = camera up (fallback to world axis if parallel),
    // t = normalize(cross(u, n)), b = normalize(cross(n, t)).
    uScratch.copy(camera.current.up).normalize();
    crossScratch.crossVectors(uScratch, nScratch);
    if (crossScratch.length() < 0.1) {
      if (Math.abs(nScratch.x) < 0.9) uScratch.set(1, 0, 0);
      else uScratch.set(0, 1, 0);
      crossScratch.crossVectors(uScratch, nScratch);
    }
    tScratch.copy(crossScratch).normalize();
    bScratch.crossVectors(nScratch, tScratch).normalize();

    // Apply rotation around normal. Positive stickerRotation is CW viewed
    // from outside the surface (matches the thumbnail + backend bake); the
    // underlying matrix below is CCW-positive, so negate the angle.
    if (stickerRotation !== 0) {
      const rad = -stickerRotation * Math.PI / 180;
      const cosR = Math.cos(rad);
      const sinR = Math.sin(rad);
      // newT = cosR*t + sinR*b; newB = -sinR*t + cosR*b.
      tmpScratch.copy(tScratch).multiplyScalar(cosR).addScaledVector(bScratch, sinR);
      bScratch.multiplyScalar(cosR).addScaledVector(tScratch, -sinR);
      tScratch.copy(tmpScratch);
    }

    // Plane geometry lies in XY with +Z as normal; set basis (t, b, n).
    basisScratch.makeBasis(tScratch, bScratch, nScratch);
    bb.quaternion.setFromRotationMatrix(basisScratch);

    // Scale to sticker size. Backend uses width = scale, height = scale * aspect.
    bb.scale.set(stickerSize, stickerSize * stickerAspect, 1);

    bb.visible = true;
    invalidate();
  }

  function handleClick(event: MouseEvent) {
    if (!active || !onPlace) return;

    const hits = raycast(event);
    if (!hits || hits.length === 0) return;

    const hit = hits[0];
    if (!(hit.object instanceof THREE.Mesh) || !hit.face) return;

    const p = hit.point;
    normalMatrixScratch.getNormalMatrix(hit.object.matrixWorld);
    nScratch.copy(hit.face.normal).applyMatrix3(normalMatrixScratch).normalize();
    uScratch.copy(camera.current.up).normalize();

    onPlace(
      [p.x, p.y, p.z],
      [nScratch.x, nScratch.y, nScratch.z],
      [uScratch.x, uScratch.y, uScratch.z],
    );
  }

  $effect(() => {
    if (!active || !(renderer instanceof WebGLRenderer)) {
      removeBillboard();
      return;
    }
    // Fresh scratch buffers per placement session.
    ndcScratch = new THREE.Vector2();
    raycasterScratch = new THREE.Raycaster();
    normalMatrixScratch = new THREE.Matrix3();
    nScratch = new THREE.Vector3();
    uScratch = new THREE.Vector3();
    tScratch = new THREE.Vector3();
    bScratch = new THREE.Vector3();
    crossScratch = new THREE.Vector3();
    tmpScratch = new THREE.Vector3();
    basisScratch = new THREE.Matrix4();

    const canvas = renderer.domElement;
    canvas.addEventListener('click', handleClick);
    canvas.addEventListener('mousemove', handleMouseMove);
    canvas.style.cursor = 'crosshair';
    return () => {
      canvas.removeEventListener('click', handleClick);
      canvas.removeEventListener('mousemove', handleMouseMove);
      canvas.style.cursor = '';
      removeBillboard();
      invalidate();
    };
  });
</script>
