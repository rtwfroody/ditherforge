package materialx

// PerlinForTest exposes perlin3D to external test packages so the
// reference permutation table can be guarded against accidental edits.
// Test-only; never reference from production code.
func PerlinForTest(x, y, z float64) float64 { return perlin3D(x, y, z) }
