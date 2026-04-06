<script lang="ts">
  import { untrack } from 'svelte';
  import { Canvas, T } from '@threlte/core';
  import { OrbitControls } from '@threlte/extras';
  import Invalidator from './Invalidator.svelte';
  import AxesGizmo from './AxesGizmo.svelte';
  import { OrbitControls as OrbitControlsImpl } from 'three/examples/jsm/controls/OrbitControls.js';
  import * as THREE from 'three';
  import { LogMessage } from '../../../wailsjs/go/main/App';

  function log(msg: string) {
    LogMessage('info', msg);
  }

  import { SharedCamera } from './SharedCamera.svelte';

  let {
    meshUrl,
    label,
    viewerId,
    camera: sharedCamera,
    brightness = 0,
    contrast = 0,
    saturation = 0,
  }: {
    meshUrl?: string;
    label: string;
    viewerId: string;
    camera: SharedCamera;
    brightness?: number;
    contrast?: number;
    saturation?: number;
  } = $props();

  // Color adjustment GLSL snippet. Must match Go's AdjustColor exactly.
  const colorAdjustGLSL = `
    uniform float uBrightness;
    uniform float uContrast;
    uniform float uSaturation;
  `;
  const colorAdjustApply = `
    {
      vec3 c = diffuseColor.rgb;
      c = c + uBrightness;
      c = (c - 0.5) * uContrast + 0.5;
      float lum = dot(c, vec3(0.2126, 0.7152, 0.0722));
      c = mix(vec3(lum), c, uSaturation);
      c = clamp(c, 0.0, 1.0);
      diffuseColor.rgb = c;
    }
  `;

  // Shared uniforms object so all materials update together.
  const colorUniforms = {
    uBrightness: { value: 0.0 },
    uContrast: { value: 1.0 },
    uSaturation: { value: 1.0 },
  };

  function createAdjustedMaterial(opts: THREE.MeshStandardMaterialParameters): THREE.MeshStandardMaterial {
    const mat = new THREE.MeshStandardMaterial(opts);
    mat.onBeforeCompile = (shader) => {
      shader.uniforms.uBrightness = colorUniforms.uBrightness;
      shader.uniforms.uContrast = colorUniforms.uContrast;
      shader.uniforms.uSaturation = colorUniforms.uSaturation;
      shader.fragmentShader = colorAdjustGLSL + shader.fragmentShader;
      shader.fragmentShader = shader.fragmentShader.replace(
        '#include <color_fragment>',
        '#include <color_fragment>\n' + colorAdjustApply,
      );
    };
    return mat;
  }

  interface SceneData {
    meshes: { geometry: THREE.BufferGeometry; material: THREE.Material }[];
  }

  // Typed-array version of MeshData for fast numeric access.
  interface TypedMeshData {
    vertices: Float32Array;
    faces: Uint32Array;
    faceColors: Uint16Array;
    uvs: Float32Array | null;
    textures: string[] | null;
    faceTextureIdx: Int32Array | null;
  }

  // Fetch binary mesh data from the backend and parse directly into typed arrays.
  // Binary format (little-endian):
  //   Header: 5x uint32 (nVertices, nFaces, nColors, nUVs, nTexIdx)
  //   float32[nVertices], uint32[nFaces], uint16[nColors],
  //   float32[nUVs], int32[nTexIdx]
  //   uint32 nTextures, then for each: uint32 length + bytes
  async function fetchBinaryMesh(url: string): Promise<TypedMeshData> {
    const resp = await fetch(url);
    if (!resp.ok) {
      throw new Error(`fetch ${url} failed: ${resp.status} ${resp.statusText}`);
    }
    const buf = await resp.arrayBuffer();
    const view = new DataView(buf);
    let offset = 0;

    const nVerts = view.getUint32(offset, true); offset += 4;
    const nFaces = view.getUint32(offset, true); offset += 4;
    const nColors = view.getUint32(offset, true); offset += 4;
    const nUVs = view.getUint32(offset, true); offset += 4;
    const nTexIdx = view.getUint32(offset, true); offset += 4;

    const vertices = new Float32Array(buf, offset, nVerts); offset += nVerts * 4;
    const faces = new Uint32Array(buf, offset, nFaces); offset += nFaces * 4;
    const faceColors = new Uint16Array(buf, offset, nColors); offset += nColors * 2;

    // After uint16 colors, offset may not be 4-byte aligned.
    // Use slice copies for subsequent arrays to handle any alignment.
    let uvs: Float32Array | null = null;
    if (nUVs > 0) {
      uvs = new Float32Array(buf.slice(offset, offset + nUVs * 4)); offset += nUVs * 4;
    }

    let faceTextureIdx: Int32Array | null = null;
    if (nTexIdx > 0) {
      faceTextureIdx = new Int32Array(buf.slice(offset, offset + nTexIdx * 4)); offset += nTexIdx * 4;
    }

    const nTex = view.getUint32(offset, true); offset += 4;
    let textures: string[] | null = null;
    if (nTex > 0) {
      textures = [];
      const decoder = new TextDecoder();
      for (let i = 0; i < nTex; i++) {
        const len = view.getUint32(offset, true); offset += 4;
        const bytes = new Uint8Array(buf, offset, len);
        textures.push(decoder.decode(bytes));
        offset += len;
      }
    }

    return { vertices, faces, faceColors, uvs, textures, faceTextureIdx };
  }

  function hasTextures(td: TypedMeshData): boolean {
    return !!(td.textures && td.uvs && td.faceTextureIdx);
  }

  async function loadTexture(base64: string): Promise<THREE.Texture> {
    const dataUrl = `data:image/jpeg;base64,${base64}`;
    const loader = new THREE.TextureLoader();
    const tex = await loader.loadAsync(dataUrl);
    tex.flipY = false;
    tex.colorSpace = THREE.SRGBColorSpace;
    return tex;
  }

  // Unpack indexed face positions into a non-indexed flat array.
  function unpackPositions(td: TypedMeshData, faceIndices: Uint32Array): Float32Array {
    const { vertices, faces } = td;
    const positions = new Float32Array(faceIndices.length * 9);
    for (let i = 0; i < faceIndices.length; i++) {
      const f = faceIndices[i];
      const i0 = faces[f * 3] * 3;
      const i1 = faces[f * 3 + 1] * 3;
      const i2 = faces[f * 3 + 2] * 3;
      const o = i * 9;

      positions[o]     = vertices[i0];
      positions[o + 1] = vertices[i0 + 1];
      positions[o + 2] = vertices[i0 + 2];
      positions[o + 3] = vertices[i1];
      positions[o + 4] = vertices[i1 + 1];
      positions[o + 5] = vertices[i1 + 2];
      positions[o + 6] = vertices[i2];
      positions[o + 7] = vertices[i2 + 1];
      positions[o + 8] = vertices[i2 + 2];
    }
    return positions;
  }

  // Unpack per-face colors into a per-vertex color array.
  function unpackFaceColors(td: TypedMeshData, faceIndices: Uint32Array): Float32Array {
    const { faceColors } = td;
    const colors = new Float32Array(faceIndices.length * 9);
    for (let i = 0; i < faceIndices.length; i++) {
      const f = faceIndices[i];
      const r = faceColors[f * 3] / 255;
      const g = faceColors[f * 3 + 1] / 255;
      const b = faceColors[f * 3 + 2] / 255;
      const o = i * 9;
      colors[o]     = r; colors[o + 1] = g; colors[o + 2] = b;
      colors[o + 3] = r; colors[o + 4] = g; colors[o + 5] = b;
      colors[o + 6] = r; colors[o + 7] = g; colors[o + 8] = b;
    }
    return colors;
  }

  // Build all-faces index for the common case of no texture grouping.
  function allFaceIndices(nFaces: number): Uint32Array {
    const indices = new Uint32Array(nFaces);
    for (let i = 0; i < nFaces; i++) indices[i] = i;
    return indices;
  }

  async function buildTexturedScene(td: TypedMeshData): Promise<SceneData> {
    const textures = td.textures!;
    const uvs = td.uvs!;
    const faceTexIdx = td.faceTextureIdx!;
    const faces = td.faces;
    const nFaces = faces.length / 3;

    // Group faces by texture index (-1 = untextured).
    const groups = new Map<number, number[]>();
    for (let f = 0; f < nFaces; f++) {
      const texId = faceTexIdx[f];
      let arr = groups.get(texId);
      if (!arr) { arr = []; groups.set(texId, arr); }
      arr.push(f);
    }

    const meshes: SceneData['meshes'] = [];

    for (const [texId, faceList] of groups) {
      const faceIndices = new Uint32Array(faceList);
      const positions = unpackPositions(td, faceIndices);

      if (texId >= 0 && texId < textures.length && textures[texId]) {
        // Textured group: use UV-mapped material.
        const faceUvs = new Float32Array(faceIndices.length * 6);

        for (let i = 0; i < faceIndices.length; i++) {
          const f = faceIndices[i];
          const i0 = faces[f * 3];
          const i1 = faces[f * 3 + 1];
          const i2 = faces[f * 3 + 2];

          faceUvs[i * 6]     = uvs[i0 * 2];
          faceUvs[i * 6 + 1] = uvs[i0 * 2 + 1];
          faceUvs[i * 6 + 2] = uvs[i1 * 2];
          faceUvs[i * 6 + 3] = uvs[i1 * 2 + 1];
          faceUvs[i * 6 + 4] = uvs[i2 * 2];
          faceUvs[i * 6 + 5] = uvs[i2 * 2 + 1];
        }

        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
        geo.setAttribute('uv', new THREE.BufferAttribute(faceUvs, 2));
        geo.computeVertexNormals();

        const tex = await loadTexture(textures[texId]);
        const mat = createAdjustedMaterial({ map: tex });
        meshes.push({ geometry: geo, material: mat });
      } else {
        // Untextured group: use face colors.
        const colors = unpackFaceColors(td, faceIndices);

        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
        geo.setAttribute('color', new THREE.BufferAttribute(colors, 3));
        geo.computeVertexNormals();

        const mat = createAdjustedMaterial({ vertexColors: true, flatShading: true });
        meshes.push({ geometry: geo, material: mat });
      }
    }

    return { meshes };
  }

  function buildFaceColorScene(td: TypedMeshData): SceneData {
    const nFaces = td.faces.length / 3;
    const faceIndices = allFaceIndices(nFaces);
    const positions = unpackPositions(td, faceIndices);
    const colors = unpackFaceColors(td, faceIndices);

    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
    geo.setAttribute('color', new THREE.BufferAttribute(colors, 3));
    geo.computeVertexNormals();

    const mat = createAdjustedMaterial({ vertexColors: true, flatShading: true });
    return { meshes: [{ geometry: geo, material: mat }] };
  }

  function computeModelBounds(td: TypedMeshData) {
    const verts = td.vertices;
    let minX = Infinity, minY = Infinity, minZ = Infinity;
    let maxX = -Infinity, maxY = -Infinity, maxZ = -Infinity;
    for (let i = 0; i < verts.length; i += 3) {
      const x = verts[i], y = verts[i + 1], z = verts[i + 2];
      if (x < minX) minX = x; if (x > maxX) maxX = x;
      if (y < minY) minY = y; if (y > maxY) maxY = y;
      if (z < minZ) minZ = z; if (z > maxZ) maxZ = z;
    }
    const center: [number, number, number] = [(minX + maxX) / 2, (minY + maxY) / 2, (minZ + maxZ) / 2];
    const size = Math.max(maxX - minX, maxY - minY, maxZ - minZ);
    return { center, size };
  }

  function computeCameraSetup(td: TypedMeshData): { position: [number, number, number]; target: [number, number, number] } {
    const { center, size } = computeModelBounds(td);
    const dist = size * 1.5;
    // Camera looks with X left-to-right, Y front-to-back, Z bottom-to-top.
    // Position: offset in +X, -Y (toward viewer), +Z (above).
    return {
      position: [center[0] + dist * 0.3, center[1] - dist * 0.8, center[2] + dist * 0.5],
      target: center,
    };
  }

  function disposeScene(scene: SceneData | null) {
    if (!scene) return;
    for (const m of scene.meshes) {
      m.geometry.dispose();
      if (m.material instanceof THREE.MeshStandardMaterial && m.material.map) {
        m.material.map.dispose();
      }
      m.material.dispose();
    }
  }

  let faceCount = $state(0);

  function formatSI(n: number): string {
    if (n >= 1e6) return (n / 1e6).toPrecision(3) + 'M';
    if (n >= 1e3) return (n / 1e3).toPrecision(3) + 'k';
    return n.toString();
  }

  let scene = $state<SceneData | null>(null);
  let cameraSetup = $state<{ position: [number, number, number]; target: [number, number, number] } | null>(null);
  let modelSize = $state(1);
  let modelCenter = $state<[number, number, number]>([0, 0, 0]);
  let controlsRef = $state<OrbitControlsImpl | undefined>(undefined);
  let hasHadCamera = false; // true once camera has been positioned

  let buildId = 0;

  $effect(() => {
    const url = meshUrl;
    const prev = untrack(() => scene);
    const myId = ++buildId;

    if (url) {
      const t0 = performance.now();
      fetchBinaryMesh(url).then((td) => {
        if (myId !== buildId) return;
        faceCount = td.faces.length / 3;

        const bounds = computeModelBounds(td);
        modelSize = bounds.size;
        modelCenter = bounds.center;
        // Only set camera on first load; keep current view on updates.
        if (!hasHadCamera) {
          cameraSetup = computeCameraSetup(td);
          hasHadCamera = true;
        }

        if (hasTextures(td)) {
          buildTexturedScene(td).then((s) => {
            log(`[${viewerId}] ${faceCount} triangles ready in ${(performance.now() - t0).toFixed(0)}ms`);
            if (myId === buildId) {
              scene = s;
              disposeScene(prev);
            } else {
              disposeScene(s);
            }
          });
        } else {
          scene = buildFaceColorScene(td);
          log(`[${viewerId}] ${faceCount} triangles ready in ${(performance.now() - t0).toFixed(0)}ms`);
          disposeScene(prev);
        }
      }).catch((err) => {
        console.error(`[${viewerId}] mesh load error:`, err);
      });
    } else {
      scene = null;
      // Keep cameraSetup so the Canvas stays mounted and OrbitControls
      // retains the user's camera orientation across reprocessing.
      if (!hasHadCamera) cameraSetup = null;
      faceCount = 0;
      disposeScene(prev);
    }

    return () => {
      disposeScene(untrack(() => scene));
    };
  });

  // Update color adjustment uniforms when props change.
  $effect(() => {
    colorUniforms.uBrightness.value = brightness / 100.0;
    colorUniforms.uContrast.value = (100.0 + contrast) / 100.0;
    colorUniforms.uSaturation.value = (100.0 + saturation) / 100.0;
  });

  // Track the last generation we applied so we don't re-apply our own updates.
  let appliedGen = 0;
  // Guards against re-entrant onchange during programmatic camera moves.
  let syncing = false;

  // Sync this viewer's camera to the shared camera state when it changes.
  $effect(() => {
    const gen = sharedCamera.generation;
    if (gen === appliedGen || !controlsRef || !scene) return;
    appliedGen = gen;

    const dist = sharedCamera.distanceRatio * modelSize;
    const x = modelCenter[0] + sharedCamera.dirX * dist;
    const y = modelCenter[1] + sharedCamera.dirY * dist;
    const z = modelCenter[2] + sharedCamera.dirZ * dist;

    syncing = true;
    controlsRef.object.position.set(x, y, z);
    controlsRef.target.set(modelCenter[0], modelCenter[1], modelCenter[2]);
    controlsRef.update();
    syncing = false;
  });

  // When the user interacts with this viewer, write to the shared camera.
  function handleControlsChange() {
    if (syncing || !controlsRef) return;
    const pos = controlsRef.object.position;
    const tgt = controlsRef.target;
    const dx = pos.x - tgt.x;
    const dy = pos.y - tgt.y;
    const dz = pos.z - tgt.z;
    const dist = Math.sqrt(dx * dx + dy * dy + dz * dz);
    if (dist < 1e-8) return;
    sharedCamera.dirX = dx / dist;
    sharedCamera.dirY = dy / dist;
    sharedCamera.dirZ = dz / dist;
    sharedCamera.distanceRatio = dist / modelSize;
    appliedGen = ++sharedCamera.generation;
  }
</script>

<div class="flex flex-col h-full">
  <div class="text-xs font-medium text-muted-foreground px-2 py-1">{label}</div>
  <div class="flex-1 rounded-md border bg-muted/30 overflow-hidden relative">
    {#if faceCount > 0}
      <div class="absolute top-2 left-2 z-10 bg-black/50 text-white text-xs px-2 py-1 rounded pointer-events-none">
        {formatSI(faceCount)} triangles
      </div>
    {/if}
    {#if scene && cameraSetup}
      <Canvas>
        <T.PerspectiveCamera
          makeDefault
          position={cameraSetup.position}
          up={[0, 0, 1]}
          fov={45}
          near={0.1}
          far={10000}
        >
          <OrbitControls
            bind:ref={controlsRef}
            target={cameraSetup.target}
            enableDamping
            onchange={handleControlsChange}
          />
        </T.PerspectiveCamera>

        <T.AmbientLight intensity={0.6} />
        <T.DirectionalLight position={[1, 2, 3]} intensity={0.8} />
        <T.DirectionalLight position={[-1, -1, -2]} intensity={0.3} />

        {#each scene.meshes as mesh}
          <T.Mesh geometry={mesh.geometry} material={mesh.material} />
        {/each}

        <Invalidator {brightness} {contrast} {saturation} />
        <AxesGizmo />
      </Canvas>
    {:else}
      <div class="flex items-center justify-center h-full text-sm text-muted-foreground">
        No model loaded
      </div>
    {/if}
  </div>
</div>
