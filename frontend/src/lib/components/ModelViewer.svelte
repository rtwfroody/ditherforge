<script lang="ts">
  import { untrack } from 'svelte';
  import { Canvas, T } from '@threlte/core';
  import { OrbitControls } from '@threlte/extras';
  import * as THREE from 'three';
  import type { pipeline } from '../../../wailsjs/go/models';

  let { meshData, label }: { meshData?: pipeline.MeshData; label: string } = $props();

  interface SceneData {
    meshes: { geometry: THREE.BufferGeometry; material: THREE.Material }[];
  }

  function hasTextures(data: pipeline.MeshData): boolean {
    return !!(data.Textures?.length && data.UVs?.length && data.FaceTextureIdx?.length);
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
  function unpackPositions(data: pipeline.MeshData, faceIndices: number[]): Float32Array {
    const positions = new Float32Array(faceIndices.length * 9);
    for (let i = 0; i < faceIndices.length; i++) {
      const f = faceIndices[i];
      const i0 = data.Faces[f * 3];
      const i1 = data.Faces[f * 3 + 1];
      const i2 = data.Faces[f * 3 + 2];

      positions[i * 9 + 0] = data.Vertices[i0 * 3];
      positions[i * 9 + 1] = data.Vertices[i0 * 3 + 1];
      positions[i * 9 + 2] = data.Vertices[i0 * 3 + 2];
      positions[i * 9 + 3] = data.Vertices[i1 * 3];
      positions[i * 9 + 4] = data.Vertices[i1 * 3 + 1];
      positions[i * 9 + 5] = data.Vertices[i1 * 3 + 2];
      positions[i * 9 + 6] = data.Vertices[i2 * 3];
      positions[i * 9 + 7] = data.Vertices[i2 * 3 + 1];
      positions[i * 9 + 8] = data.Vertices[i2 * 3 + 2];
    }
    return positions;
  }

  // Unpack per-face colors into a per-vertex color array.
  function unpackFaceColors(data: pipeline.MeshData, faceIndices: number[]): Float32Array {
    const colors = new Float32Array(faceIndices.length * 9);
    for (let i = 0; i < faceIndices.length; i++) {
      const f = faceIndices[i];
      const r = data.FaceColors[f * 3] / 255;
      const g = data.FaceColors[f * 3 + 1] / 255;
      const b = data.FaceColors[f * 3 + 2] / 255;
      colors[i * 9 + 0] = r; colors[i * 9 + 1] = g; colors[i * 9 + 2] = b;
      colors[i * 9 + 3] = r; colors[i * 9 + 4] = g; colors[i * 9 + 5] = b;
      colors[i * 9 + 6] = r; colors[i * 9 + 7] = g; colors[i * 9 + 8] = b;
    }
    return colors;
  }

  async function buildTexturedScene(data: pipeline.MeshData): Promise<SceneData> {
    const textures = data.Textures!;
    const uvs = data.UVs!;
    const faceTexIdx = data.FaceTextureIdx!;
    const nFaces = data.Faces.length / 3;

    // Group faces by texture index (-1 = untextured).
    const groups = new Map<number, number[]>();
    for (let f = 0; f < nFaces; f++) {
      const texId = faceTexIdx[f];
      let arr = groups.get(texId);
      if (!arr) { arr = []; groups.set(texId, arr); }
      arr.push(f);
    }

    const meshes: SceneData['meshes'] = [];

    for (const [texId, faceIndices] of groups) {
      const positions = unpackPositions(data, faceIndices);

      if (texId >= 0 && texId < textures.length && textures[texId]) {
        // Textured group: use UV-mapped material.
        const faceUvs = new Float32Array(faceIndices.length * 6);

        for (let i = 0; i < faceIndices.length; i++) {
          const f = faceIndices[i];
          const i0 = data.Faces[f * 3];
          const i1 = data.Faces[f * 3 + 1];
          const i2 = data.Faces[f * 3 + 2];

          faceUvs[i * 6 + 0] = uvs[i0 * 2];
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
        const mat = new THREE.MeshStandardMaterial({ map: tex });
        meshes.push({ geometry: geo, material: mat });
      } else {
        // Untextured group: use face colors.
        const colors = unpackFaceColors(data, faceIndices);

        const geo = new THREE.BufferGeometry();
        geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
        geo.setAttribute('color', new THREE.BufferAttribute(colors, 3));
        geo.computeVertexNormals();

        const mat = new THREE.MeshStandardMaterial({ vertexColors: true, flatShading: true });
        meshes.push({ geometry: geo, material: mat });
      }
    }

    return { meshes };
  }

  function buildFaceColorScene(data: pipeline.MeshData): SceneData {
    const nFaces = data.Faces.length / 3;
    const allFaces = Array.from({ length: nFaces }, (_, i) => i);
    const positions = unpackPositions(data, allFaces);
    const colors = unpackFaceColors(data, allFaces);

    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
    geo.setAttribute('color', new THREE.BufferAttribute(colors, 3));
    geo.computeVertexNormals();

    const mat = new THREE.MeshStandardMaterial({ vertexColors: true, flatShading: true });
    return { meshes: [{ geometry: geo, material: mat }] };
  }

  function computeCameraSetup(data: pipeline.MeshData): { position: [number, number, number]; target: [number, number, number] } {
    let minX = Infinity, minY = Infinity, minZ = Infinity;
    let maxX = -Infinity, maxY = -Infinity, maxZ = -Infinity;
    const verts = data.Vertices;
    for (let i = 0; i < verts.length; i += 3) {
      minX = Math.min(minX, verts[i]);
      maxX = Math.max(maxX, verts[i]);
      minY = Math.min(minY, verts[i + 1]);
      maxY = Math.max(maxY, verts[i + 1]);
      minZ = Math.min(minZ, verts[i + 2]);
      maxZ = Math.max(maxZ, verts[i + 2]);
    }
    const cx = (minX + maxX) / 2;
    const cy = (minY + maxY) / 2;
    const cz = (minZ + maxZ) / 2;
    const size = Math.max(maxX - minX, maxY - minY, maxZ - minZ);
    const dist = size * 1.5;
    return {
      position: [cx + dist * 0.5, cy + dist * 0.5, cz + dist],
      target: [cx, cy, cz],
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

  let scene = $state<SceneData | null>(null);
  let cameraSetup = $state<{ position: [number, number, number]; target: [number, number, number] } | null>(null);

  let buildId = 0;

  $effect(() => {
    const data = meshData;
    const prev = untrack(() => scene);
    const myId = ++buildId;

    if (data) {
      cameraSetup = computeCameraSetup(data);

      if (hasTextures(data)) {
        buildTexturedScene(data).then((s) => {
          if (myId === buildId) {
            scene = s;
            disposeScene(prev);
          } else {
            disposeScene(s);
          }
        });
      } else {
        scene = buildFaceColorScene(data);
        disposeScene(prev);
      }
    } else {
      scene = null;
      cameraSetup = null;
      disposeScene(prev);
    }

    return () => {
      disposeScene(untrack(() => scene));
    };
  });
</script>

<div class="flex flex-col h-full">
  <div class="text-xs font-medium text-muted-foreground px-2 py-1">{label}</div>
  <div class="flex-1 rounded-md border bg-muted/30 overflow-hidden">
    {#if scene && cameraSetup}
      <Canvas>
        <T.PerspectiveCamera
          makeDefault
          position={cameraSetup.position}
          fov={45}
          near={0.1}
          far={10000}
        >
          <OrbitControls target={cameraSetup.target} enableDamping />
        </T.PerspectiveCamera>

        <T.AmbientLight intensity={0.6} />
        <T.DirectionalLight position={[1, 2, 3]} intensity={0.8} />
        <T.DirectionalLight position={[-1, -1, -2]} intensity={0.3} />

        {#each scene.meshes as mesh}
          <T.Mesh geometry={mesh.geometry} material={mesh.material} />
        {/each}
      </Canvas>
    {:else}
      <div class="flex items-center justify-center h-full text-sm text-muted-foreground">
        No model loaded
      </div>
    {/if}
  </div>
</div>
