<script lang="ts">
  import { untrack } from 'svelte';
  import { Canvas, T } from '@threlte/core';
  import { OrbitControls } from '@threlte/extras';
  import Invalidator from './Invalidator.svelte';
  import AxesGizmo from './AxesGizmo.svelte';
  import ColorPicker3D from './ColorPicker3D.svelte';
  import { OrbitControls as OrbitControlsImpl } from 'three/examples/jsm/controls/OrbitControls.js';
  import * as THREE from 'three';
  import { LogMessage } from '../../../wailsjs/go/main/App';
  import { LoaderCircleIcon } from '@lucide/svelte';


  function log(msg: string) {
    LogMessage('info', msg);
  }

  import { SharedCamera } from './SharedCamera.svelte';

  type WarpPin = { sourceHex: string; targetHex: string; sigma: number };

  let {
    meshUrl,
    label,
    viewerId,
    camera: sharedCamera,
    errorMessage,
    brightness = 0,
    contrast = 0,
    saturation = 0,
    pickMode = false,
    onColorPick,
    warpPins = [],
    loading = '',
  }: {
    meshUrl?: string;
    label: string;
    viewerId: string;
    camera: SharedCamera;
    errorMessage?: string;
    brightness?: number;
    contrast?: number;
    saturation?: number;
    pickMode?: boolean;
    onColorPick?: (hex: string) => void;
    warpPins?: WarpPin[];
    loading?: string;
  } = $props();

  // Color adjustment GLSL snippet. Must match Go's AdjustColor exactly.
  // Go operates on sRGB values, so we convert linear→sRGB before adjusting,
  // then sRGB→linear after, since Three.js works in linear space.
  const colorAdjustGLSL = `
    uniform float uBrightness;
    uniform float uContrast;
    uniform float uSaturation;

    float adjLinearToSRGB(float c) {
      return c <= 0.0031308 ? 12.92 * c : 1.055 * pow(c, 1.0 / 2.4) - 0.055;
    }
    float adjSRGBToLinear(float c) {
      return c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4);
    }
  `;
  const colorAdjustApply = `
    {
      // Convert to sRGB for adjustment (matching Go's AdjustColor).
      vec3 c = vec3(adjLinearToSRGB(diffuseColor.r), adjLinearToSRGB(diffuseColor.g), adjLinearToSRGB(diffuseColor.b));
      c = c + uBrightness;
      c = (c - 0.5) * uContrast + 0.5;
      float lum = dot(c, vec3(0.2126, 0.7152, 0.0722));
      c = mix(vec3(lum), c, uSaturation);
      c = clamp(c, 0.0, 1.0);
      // Convert back to linear for Three.js lighting.
      diffuseColor.rgb = vec3(adjSRGBToLinear(c.r), adjSRGBToLinear(c.g), adjSRGBToLinear(c.b));
    }
  `;

  // Shared uniforms object so all materials update together.
  const colorUniforms = {
    uBrightness: { value: 0.0 },
    uContrast: { value: 1.0 },
    uSaturation: { value: 1.0 },
  };

  // --- Color Warp Preview (full RBF in GLSL) ---
  // This is a JS/GLSL mirror of the Go backend in internal/voxel/colorwarp.go.
  // If you change the RBF kernel, Lab conversion, or sigma scaling here,
  // update the Go implementation to match (and vice versa).

  const MAX_WARP_PINS = 8;

  const warpUniforms = {
    uWarpPinCount: { value: 0 },
    uWarpSourceLab: { value: Array.from({length: MAX_WARP_PINS}, () => new THREE.Vector3()) },
    uWarpWeights: { value: Array.from({length: MAX_WARP_PINS}, () => new THREE.Vector3()) },
    uWarpSigmas: { value: new Array(MAX_WARP_PINS).fill(1.0) },
  };

  // GLSL declarations: uniforms + Lab conversion + RBF evaluation.
  const warpGLSL = `
    const int MAX_WARP_PINS = 8;
    uniform int uWarpPinCount;
    uniform vec3 uWarpSourceLab[MAX_WARP_PINS];
    uniform vec3 uWarpWeights[MAX_WARP_PINS];
    uniform float uWarpSigmas[MAX_WARP_PINS];

    const vec3 WARP_D65 = vec3(0.95047, 1.0, 1.08883);

    float warpLabF(float t) {
      float delta3 = 0.00885645; // (6/29)^3
      float scale = 7.787037;    // 1/(3*(6/29)^2)
      return t > delta3 ? pow(t, 1.0 / 3.0) : t * scale + 4.0 / 29.0;
    }

    vec3 warpLinearRGBtoLab(vec3 rgb) {
      float x = 0.4124564 * rgb.r + 0.3575761 * rgb.g + 0.1804375 * rgb.b;
      float y = 0.2126729 * rgb.r + 0.7151522 * rgb.g + 0.0721750 * rgb.b;
      float z = 0.0193339 * rgb.r + 0.1191920 * rgb.g + 0.9503041 * rgb.b;
      float fx = warpLabF(x / WARP_D65.x);
      float fy = warpLabF(y / WARP_D65.y);
      float fz = warpLabF(z / WARP_D65.z);
      return vec3(1.16 * fy - 0.16, 5.0 * (fx - fy), 2.0 * (fy - fz));
    }

    float warpLabFInv(float t) {
      float delta = 6.0 / 29.0;
      return t > delta ? t * t * t : 3.0 * delta * delta * (t - 4.0 / 29.0);
    }

    vec3 warpLabToLinearRGB(vec3 lab) {
      float fy = (lab.x + 0.16) / 1.16;
      float fx = fy + lab.y / 5.0;
      float fz = fy - lab.z / 2.0;
      float x = WARP_D65.x * warpLabFInv(fx);
      float y = WARP_D65.y * warpLabFInv(fy);
      float z = WARP_D65.z * warpLabFInv(fz);
      float r =  3.2404542 * x - 1.5371385 * y - 0.4985314 * z;
      float g = -0.9692660 * x + 1.8760108 * y + 0.0415560 * z;
      float b =  0.0556434 * x - 0.2040259 * y + 1.0572252 * z;
      return clamp(vec3(r, g, b), 0.0, 1.0);
    }
  `;

  // Applied after brightness/contrast/saturation adjustment.
  // Accumulates the warp delta separately (matching Go's eval), so that
  // each pin's distance is computed from the original Lab color.
  const warpApply = `
    {
      if (uWarpPinCount > 0) {
        vec3 lab = warpLinearRGBtoLab(diffuseColor.rgb);
        vec3 warpDelta = vec3(0.0);
        for (int i = 0; i < MAX_WARP_PINS; i++) {
          if (i >= uWarpPinCount) break;
          vec3 diff = lab - uWarpSourceLab[i];
          float rSq = dot(diff, diff) / (uWarpSigmas[i] * uWarpSigmas[i]);
          if (rSq < 1.0) {
            float phi = exp(-4.5 * rSq);
            warpDelta += uWarpWeights[i] * phi;
          }
        }
        diffuseColor.rgb = warpLabToLinearRGB(lab + warpDelta);
      }
    }
  `;

  // --- JS-side RBF solver (mirrors Go's colorwarp.go) ---

  function hexToLab(hex: string): [number, number, number] | null {
    if (!/^#[0-9a-fA-F]{6}$/.test(hex)) return null;
    const r = srgbToLinear(parseInt(hex.slice(1, 3), 16) / 255);
    const g = srgbToLinear(parseInt(hex.slice(3, 5), 16) / 255);
    const b = srgbToLinear(parseInt(hex.slice(5, 7), 16) / 255);
    const x = 0.4124564*r + 0.3575761*g + 0.1804375*b;
    const y = 0.2126729*r + 0.7151522*g + 0.0721750*b;
    const z = 0.0193339*r + 0.1191920*g + 0.9503041*b;
    const delta = 6/29;
    const delta3 = delta*delta*delta;
    const f = (t: number) => t > delta3 ? Math.cbrt(t) : t/(3*delta*delta) + 4/29;
    const fx = f(x / 0.95047);
    const fy = f(y / 1.0);
    const fz = f(z / 1.08883);
    return [1.16*fy - 0.16, 5.0*(fx - fy), 2.0*(fy - fz)];
  }

  function jsGaussElim(A: number[][], b: number[]): number[] | null {
    const n = b.length;
    const aug = A.map((row, i) => [...row, b[i]]);
    for (let col = 0; col < n; col++) {
      let maxRow = col, maxVal = Math.abs(aug[col][col]);
      for (let row = col + 1; row < n; row++) {
        const v = Math.abs(aug[row][col]);
        if (v > maxVal) { maxVal = v; maxRow = row; }
      }
      if (maxVal < 1e-12) return null;
      [aug[col], aug[maxRow]] = [aug[maxRow], aug[col]];
      const pivot = aug[col][col];
      for (let row = col + 1; row < n; row++) {
        const factor = aug[row][col] / pivot;
        for (let j = col; j <= n; j++) aug[row][j] -= factor * aug[col][j];
      }
    }
    const x = new Array(n);
    for (let i = n - 1; i >= 0; i--) {
      x[i] = aug[i][n];
      for (let j = i + 1; j < n; j++) x[i] -= aug[i][j] * x[j];
      x[i] /= aug[i][i];
    }
    return x;
  }

  function computeAutoSigma(sources: [number, number, number][]): number {
    if (sources.length <= 1) return 0.3; // 30 delta-E / 100
    const dists: number[] = [];
    for (let i = 0; i < sources.length; i++) {
      for (let j = i + 1; j < sources.length; j++) {
        const dL = sources[i][0] - sources[j][0];
        const da = sources[i][1] - sources[j][1];
        const db = sources[i][2] - sources[j][2];
        dists.push(Math.sqrt(dL*dL + da*da + db*db));
      }
    }
    dists.sort((a, b) => a - b);
    let median = dists[Math.floor(dists.length / 2)];
    if (median < 0.05) median = 0.05;
    return median / 2;
  }

  function solveAndUpdateWarpUniforms(pins: WarpPin[]) {
    const valid = pins.filter(p =>
      /^#[0-9a-fA-F]{6}$/.test(p.sourceHex) && /^#[0-9a-fA-F]{6}$/.test(p.targetHex)
    ).slice(0, MAX_WARP_PINS);

    if (valid.length === 0) {
      warpUniforms.uWarpPinCount.value = 0;
      return;
    }

    const sources = valid.map(p => hexToLab(p.sourceHex)!);
    const targets = valid.map(p => hexToLab(p.targetHex)!);
    const deltas: [number, number, number][] = sources.map((s, i) => [
      targets[i][0] - s[0], targets[i][1] - s[1], targets[i][2] - s[2],
    ]);

    const defaultSigma = computeAutoSigma(sources);
    const sigmas = valid.map(p => p.sigma > 0 ? p.sigma / 100 : defaultSigma);

    // Build Phi matrix: Gaussian with hard cutoff at reach radius.
    // phi(r) = exp(-4.5 * r²) for r < 1, else 0, where r = dist/sigma.
    const n = valid.length;
    const phi: number[][] = [];
    for (let i = 0; i < n; i++) {
      phi[i] = [];
      for (let j = 0; j < n; j++) {
        const dL = sources[i][0] - sources[j][0];
        const da = sources[i][1] - sources[j][1];
        const db = sources[i][2] - sources[j][2];
        const rSq = (dL*dL + da*da + db*db) / (sigmas[j] * sigmas[j]);
        phi[i][j] = rSq >= 1 ? 0 : Math.exp(-4.5 * rSq);
      }
    }

    // Solve for weights per channel.
    const weights: [number, number, number][] = Array.from({length: n}, () => [0, 0, 0]);
    for (let ch = 0; ch < 3; ch++) {
      const rhs = deltas.map(d => d[ch]);
      const w = jsGaussElim(phi, rhs);
      if (!w) { warpUniforms.uWarpPinCount.value = 0; return; }
      for (let i = 0; i < n; i++) weights[i][ch] = w[i];
    }

    // Update uniforms.
    warpUniforms.uWarpPinCount.value = n;
    for (let i = 0; i < MAX_WARP_PINS; i++) {
      if (i < n) {
        warpUniforms.uWarpSourceLab.value[i].set(sources[i][0], sources[i][1], sources[i][2]);
        warpUniforms.uWarpWeights.value[i].set(weights[i][0], weights[i][1], weights[i][2]);
        warpUniforms.uWarpSigmas.value[i] = sigmas[i];
      }
    }
  }

  function createAdjustedMaterial(opts: THREE.MeshStandardMaterialParameters): THREE.MeshStandardMaterial {
    const mat = new THREE.MeshStandardMaterial(opts);
    mat.onBeforeCompile = (shader) => {
      shader.uniforms.uBrightness = colorUniforms.uBrightness;
      shader.uniforms.uContrast = colorUniforms.uContrast;
      shader.uniforms.uSaturation = colorUniforms.uSaturation;
      shader.uniforms.uWarpPinCount = warpUniforms.uWarpPinCount;
      shader.uniforms.uWarpSourceLab = warpUniforms.uWarpSourceLab;
      shader.uniforms.uWarpWeights = warpUniforms.uWarpWeights;
      shader.uniforms.uWarpSigmas = warpUniforms.uWarpSigmas;
      shader.fragmentShader = colorAdjustGLSL + warpGLSL + shader.fragmentShader;
      shader.fragmentShader = shader.fragmentShader.replace(
        '#include <color_fragment>',
        '#include <color_fragment>\n' + colorAdjustApply + '\n' + warpApply,
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

  async function loadTexture(encoded: string): Promise<THREE.Texture> {
    let mime = 'image/jpeg';
    let base64 = encoded;
    if (encoded.startsWith('png:')) {
      mime = 'image/png';
      base64 = encoded.slice(4);
    } else if (encoded.startsWith('jpeg:')) {
      base64 = encoded.slice(5);
    }
    const dataUrl = `data:${mime};base64,${base64}`;
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

  // Convert sRGB [0,1] to linear [0,1]. Three.js treats vertex colors as
  // linear, so we must do this conversion ourselves (unlike textures which
  // have colorSpace = SRGBColorSpace and are converted by the GPU).
  function srgbToLinear(c: number): number {
    return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  }

  // Unpack per-face colors into a per-vertex color array (linear space).
  function unpackFaceColors(td: TypedMeshData, faceIndices: Uint32Array): Float32Array {
    const { faceColors } = td;
    const colors = new Float32Array(faceIndices.length * 9);
    for (let i = 0; i < faceIndices.length; i++) {
      const f = faceIndices[i];
      const r = srgbToLinear(faceColors[f * 3] / 255);
      const g = srgbToLinear(faceColors[f * 3 + 1] / 255);
      const b = srgbToLinear(faceColors[f * 3 + 2] / 255);
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
    if (verts.length === 0) {
      return { center: [0, 0, 0] as [number, number, number], size: 0 };
    }
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

  function computeCameraSetup(td: TypedMeshData): { position: [number, number, number]; target: [number, number, number]; near: number; far: number } {
    const { center, size: rawSize } = computeModelBounds(td);
    const size = Math.max(rawSize, 1e-4); // prevent degenerate camera for zero-size models
    const dist = size * 1.5;
    // Camera looks with X left-to-right, Y front-to-back, Z bottom-to-top.
    // Position: offset in +X, -Y (toward viewer), +Z (above).
    return {
      position: [center[0] + dist * 0.3, center[1] - dist * 0.8, center[2] + dist * 0.5],
      target: center,
      near: size * 0.001,
      far: size * 20,
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
  let cameraSetup = $state<{ position: [number, number, number]; target: [number, number, number]; near: number; far: number } | null>(null);
  let controlsRef = $state<OrbitControlsImpl | undefined>(undefined);

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

        // If no viewer has set up the camera yet, compute initial pose from
        // this mesh's geometry. Otherwise, use the shared camera state.
        if (!sharedCamera.initialized) {
          const setup = computeCameraSetup(td);
          sharedCamera.posX = setup.position[0];
          sharedCamera.posY = setup.position[1];
          sharedCamera.posZ = setup.position[2];
          sharedCamera.near = setup.near;
          sharedCamera.far = setup.far;
          sharedCamera.targetX = setup.target[0];
          sharedCamera.targetY = setup.target[1];
          sharedCamera.targetZ = setup.target[2];
          sharedCamera.initialized = true;
          ++sharedCamera.generation;
        }
        cameraSetup = {
          position: [sharedCamera.posX, sharedCamera.posY, sharedCamera.posZ],
          target: [sharedCamera.targetX, sharedCamera.targetY, sharedCamera.targetZ],
          near: sharedCamera.near,
          far: sharedCamera.far,
        };
        appliedGen = sharedCamera.generation;
        // Suppress handleControlsChange during OrbitControls mount so it
        // doesn't write the freshly-applied values back to sharedCamera.
        mountSyncing = true;
        requestAnimationFrame(() => requestAnimationFrame(() => { mountSyncing = false; }));

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
      // Keep cameraSetup around so the Canvas can remount with the current
      // shared camera state rather than re-deriving from mesh geometry.
      if (!sharedCamera.initialized) cameraSetup = null;
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

  // Recompute RBF weights and update warp uniforms when pins change.
  $effect(() => {
    solveAndUpdateWarpUniforms(warpPins);
  });

  // Track the last generation we applied so we don't re-apply our own updates.
  let appliedGen = 0;
  // Guards against re-entrant onchange during programmatic camera moves.
  let syncing = false;
  // Suppresses handleControlsChange during OrbitControls mount (separate from
  // syncing so the sync effect doesn't accidentally clear it).
  let mountSyncing = false;

  // Sync this viewer's camera to the shared camera state when it changes.
  $effect(() => {
    const gen = sharedCamera.generation;
    if (gen === appliedGen || !controlsRef || !scene) return;
    appliedGen = gen;

    syncing = true;
    controlsRef.object.position.set(sharedCamera.posX, sharedCamera.posY, sharedCamera.posZ);
    controlsRef.target.set(sharedCamera.targetX, sharedCamera.targetY, sharedCamera.targetZ);
    controlsRef.update();
    syncing = false;
  });

  // When the user interacts with this viewer, write to the shared camera.
  // Skip if position/target haven't actually changed (e.g. OrbitControls
  // firing onchange during mount with the same values we just set).
  const EPS = 1e-6;
  function handleControlsChange() {
    if (syncing || mountSyncing || !controlsRef) return;
    const pos = controlsRef.object.position;
    const tgt = controlsRef.target;
    if (Math.abs(pos.x - sharedCamera.posX) < EPS &&
        Math.abs(pos.y - sharedCamera.posY) < EPS &&
        Math.abs(pos.z - sharedCamera.posZ) < EPS &&
        Math.abs(tgt.x - sharedCamera.targetX) < EPS &&
        Math.abs(tgt.y - sharedCamera.targetY) < EPS &&
        Math.abs(tgt.z - sharedCamera.targetZ) < EPS) return;
    sharedCamera.posX = pos.x;
    sharedCamera.posY = pos.y;
    sharedCamera.posZ = pos.z;
    sharedCamera.targetX = tgt.x;
    sharedCamera.targetY = tgt.y;
    sharedCamera.targetZ = tgt.z;
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
          near={cameraSetup.near}
          far={cameraSetup.far}
        >
          <OrbitControls
            bind:ref={controlsRef}
            target={cameraSetup.target}
            enableDamping
            enabled={!pickMode}
            onchange={handleControlsChange}
          />
        </T.PerspectiveCamera>

        <T.AmbientLight intensity={0.6} />
        <T.DirectionalLight position={[1, 2, 3]} intensity={0.8} />
        <T.DirectionalLight position={[-1, -1, -2]} intensity={0.3} />

        {#each scene.meshes as mesh}
          <T.Mesh geometry={mesh.geometry} material={mesh.material} />
        {/each}

        <Invalidator {brightness} {contrast} {saturation} extra={JSON.stringify(warpPins)} />
        <AxesGizmo />
        <ColorPicker3D {pickMode} onPick={onColorPick} {brightness} {contrast} {saturation} />
      </Canvas>
    {:else if errorMessage}
      <div class="flex items-center justify-center h-full text-sm text-red-400 p-4 text-center">
        {errorMessage}
      </div>
    {:else if loading}
      <div class="flex items-center justify-center h-full text-sm text-muted-foreground gap-2">
        <LoaderCircleIcon class="w-4 h-4 animate-spin" />
        Loading {loading}
      </div>
    {:else}
      <div class="flex items-center justify-center h-full text-sm text-muted-foreground">
        No model loaded
      </div>
    {/if}
  </div>
</div>
