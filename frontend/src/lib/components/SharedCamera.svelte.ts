// Shared camera state — single source of truth for synced 3D viewers.
// Stores absolute world-space camera position and target.
// Both viewers show models in the same coordinate space, so absolute
// coordinates work directly. The `generation` counter lets each viewer
// skip updates it caused itself.
export class SharedCamera {
  posX = $state(0);
  posY = $state(0);
  posZ = $state(0);
  targetX = $state(0);
  targetY = $state(0);
  targetZ = $state(0);
  initialized = $state(false);
  generation = $state(0);
}
