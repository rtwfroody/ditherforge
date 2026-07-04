//go:build !tbbcontrol

package manifoldbool

// SetMaxParallelism is a no-op in builds without the `tbbcontrol` tag. Those
// builds link a Manifold compiled with MANIFOLD_PAR=OFF (the CI/release
// configuration), so its Boolean uses no TBB backend and there is nothing to
// pin — and no TBB header/library is available to compile the real control.
// See tbbctl_on.go for the TBB-backed implementation.
func SetMaxParallelism(n int) {}
