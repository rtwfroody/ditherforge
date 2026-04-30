//go:build !cgal

package cgalclip

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// HasCGAL reports whether the binary was built with CGAL support.
// In this fallback file (no cgal build tag), it's false.
const HasCGAL = false

// doClip is the no-CGAL stub. The Split feature requires CGAL — there
// is no fallback because the prior naive cut wasn't reliable enough.
// Build with `-tags cgal` (which the release workflow does on every
// platform) to enable.
func doClip(_ *loader.LoadedModel, _ [3]float64, _ float64) (*loader.LoadedModel, error) {
	return nil, fmt.Errorf("cgalclip: built without the cgal build tag — Split requires CGAL; rebuild with -tags cgal")
}
