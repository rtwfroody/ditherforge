// Shared frontend types.

// Cut-plane preview overlay payload. Mirrors the backend's
// pipeline.SplitPreviewResult shape (see internal/pipeline/splitpreview.go),
// but is currently computed client-side from the input-mesh event's bbox
// to avoid RPC churn on the Split offset slider. Coordinates are in the
// rendered-mesh frame (i.e. already scaled by previewScale).
export type CutPlanePreview = {
  origin: [number, number, number];
  normal: [number, number, number];
  u: [number, number, number];
  v: [number, number, number];
  halfExtentU: number;
  halfExtentV: number;
};
