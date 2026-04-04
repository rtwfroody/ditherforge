<script lang="ts">
  import { Canvas, T } from '@threlte/core';
  import { OrbitControls } from '@threlte/extras';
  import * as THREE from 'three';
  import type { pipeline } from '../../../wailsjs/go/models';

  let { meshData, label }: { meshData?: pipeline.MeshData; label: string } = $props();

  function buildGeometry(data: pipeline.MeshData): THREE.BufferGeometry {
    const nFaces = data.Faces.length / 3;
    const positions = new Float32Array(nFaces * 9);
    const colors = new Float32Array(nFaces * 9);

    for (let f = 0; f < nFaces; f++) {
      const i0 = data.Faces[f * 3];
      const i1 = data.Faces[f * 3 + 1];
      const i2 = data.Faces[f * 3 + 2];

      positions[f * 9 + 0] = data.Vertices[i0 * 3];
      positions[f * 9 + 1] = data.Vertices[i0 * 3 + 1];
      positions[f * 9 + 2] = data.Vertices[i0 * 3 + 2];
      positions[f * 9 + 3] = data.Vertices[i1 * 3];
      positions[f * 9 + 4] = data.Vertices[i1 * 3 + 1];
      positions[f * 9 + 5] = data.Vertices[i1 * 3 + 2];
      positions[f * 9 + 6] = data.Vertices[i2 * 3];
      positions[f * 9 + 7] = data.Vertices[i2 * 3 + 1];
      positions[f * 9 + 8] = data.Vertices[i2 * 3 + 2];

      const r = data.FaceColors[f * 3] / 255;
      const g = data.FaceColors[f * 3 + 1] / 255;
      const b = data.FaceColors[f * 3 + 2] / 255;
      colors[f * 9 + 0] = r; colors[f * 9 + 1] = g; colors[f * 9 + 2] = b;
      colors[f * 9 + 3] = r; colors[f * 9 + 4] = g; colors[f * 9 + 5] = b;
      colors[f * 9 + 6] = r; colors[f * 9 + 7] = g; colors[f * 9 + 8] = b;
    }

    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(positions, 3));
    geo.setAttribute('color', new THREE.BufferAttribute(colors, 3));
    geo.computeVertexNormals();
    return geo;
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

  let geometry = $state<THREE.BufferGeometry | null>(null);
  let cameraSetup = $state<{ position: [number, number, number]; target: [number, number, number] } | null>(null);

  $effect(() => {
    const prev = geometry;
    if (meshData) {
      geometry = buildGeometry(meshData);
      cameraSetup = computeCameraSetup(meshData);
    } else {
      geometry = null;
      cameraSetup = null;
    }
    prev?.dispose();
  });
</script>

<div class="flex flex-col h-full">
  <div class="text-xs font-medium text-muted-foreground px-2 py-1">{label}</div>
  <div class="flex-1 rounded-md border bg-muted/30 overflow-hidden">
    {#if geometry && cameraSetup}
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

        <T.Mesh {geometry}>
          <T.MeshStandardMaterial vertexColors flatShading />
        </T.Mesh>
      </Canvas>
    {:else}
      <div class="flex items-center justify-center h-full text-sm text-muted-foreground">
        No model loaded
      </div>
    {/if}
  </div>
</div>
