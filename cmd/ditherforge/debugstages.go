package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// writeDebugStages dumps intermediate-result renders for hunting surface
// defects (the Nord Stage 4 flat-surface white holes). For each view it
// writes three PNGs of the final output mesh:
//
//   - <view>_unculled.png: every triangle, front- and back-facing alike.
//   - <view>_culled.png:   THREE.FrontSide back-face culling, exactly what
//     the GUI viewport shows.
//   - <view>_holes.png:    the culled render, but every pixel that the
//     unculled render covered and the culled render did NOT is painted
//     magenta. Those magenta pixels are precisely the holes the user sees:
//     a surface is there (unculled has it) but its front face points away
//     from the camera (culled drops it). Magenta wedges in the middle of a
//     flat panel ⇒ inverted faces. Gaps that are blank in BOTH renders,
//     surrounded by surface ⇒ genuinely MISSING geometry.
//
// The decisive top-down view ({0,90}) is always rendered; the caller's
// view set covers the rest. Rendering the output mesh needs no cache or
// export, so this runs even when those paths are broken.
func writeDebugStages(dir string, mesh *pipeline.MeshData, res int) error {
	if mesh == nil {
		return fmt.Errorf("no output mesh to render")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	views := []debugrender.View{
		{Name: "top", Azimuth: 0, Elev: 90},
		{Name: "bottom", Azimuth: 0, Elev: -90},
		{Name: "persp", Azimuth: 45, Elev: 25},
	}

	for _, v := range views {
		// Shared bounds so the culled and unculled rasters line up
		// pixel-for-pixel (RenderPipelineMesh uses these same projected
		// bounds internally).
		bounds := debugrender.MeshDataProjectedBounds(mesh, v)
		unculled := debugrender.RenderPipelineMesh(mesh, v, res)
		culled := debugrender.RenderPipelineMeshCulledWithBounds(mesh, v, res, bounds)

		if err := debugrender.WritePNG(filepath.Join(dir, v.Name+"_unculled.png"), unculled); err != nil {
			return err
		}
		if err := debugrender.WritePNG(filepath.Join(dir, v.Name+"_culled.png"), culled.ToRGBA()); err != nil {
			return err
		}

		holes, nHole, nUnculled := holesOverlay(unculled, culled)
		if err := debugrender.WritePNG(filepath.Join(dir, v.Name+"_holes.png"), holes); err != nil {
			return err
		}
		pct := 0.0
		if nUnculled > 0 {
			pct = 100 * float64(nHole) / float64(nUnculled)
		}
		fmt.Printf("  debug-stages %-7s: %d culled-away (hole) px / %d surface px (%.3f%%)\n",
			v.Name, nHole, nUnculled, pct)
	}
	fmt.Printf("Wrote output-mesh stage renders to %s\n", dir)
	return nil
}

// holesOverlay paints the culled render and overlays magenta on every pixel
// the unculled render covered but the culled one dropped (a surface whose
// front face points away — a viewport hole). Returns the overlay plus the
// culled-away pixel count and the total unculled (surface) pixel count.
func holesOverlay(unculled *image.RGBA, culled *render.ColorImage) (*image.RGBA, int, int) {
	w, h := culled.Width, culled.Height
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	magenta := color.RGBA{255, 0, 255, 255}
	var nHole, nUnculled int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			// Unculled coverage: the RGBA encoder writes alpha 255 only
			// where a triangle (either facing) was rasterized.
			_, _, _, ua := unculled.At(x, y).RGBA()
			hasUnculled := ua > 0
			hasCulled := culled.HasPixel[i]
			if hasUnculled {
				nUnculled++
			}
			switch {
			case hasCulled:
				out.SetRGBA(x, y, color.RGBA{culled.R[i], culled.G[i], culled.B[i], 255})
			case hasUnculled:
				// Surface present but front-culled away → the hole.
				out.SetRGBA(x, y, magenta)
				nHole++
			}
		}
	}
	return out, nHole, nUnculled
}
