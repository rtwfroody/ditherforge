// Shared camera state — single source of truth for synced 3D viewers.
// Stores camera direction as a unit vector (from target toward camera)
// plus distance as a ratio of model size.  Avoids spherical-coordinate
// ambiguities with non-default up vectors.
// The `generation` counter lets each viewer skip updates it caused itself.
// Default camera offset — must match computeCameraSetup in ModelViewer.svelte.
const _dx = 0.3, _dy = -0.8, _dz = 0.5;
const _mag = Math.sqrt(_dx * _dx + _dy * _dy + _dz * _dz);

export class SharedCamera {
  // Unit vector: direction from target to camera.
  dirX = $state(_dx / _mag);
  dirY = $state(_dy / _mag);
  dirZ = $state(_dz / _mag);
  distanceRatio = $state(1.5 * _mag);
  generation = $state(0);
}
