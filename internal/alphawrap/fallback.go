//go:build !cgal

package alphawrap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/hschendel/stl"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// hasCGAL reports whether this binary was built with CGO-based CGAL support.
var hasCGAL = false

// ErrNoUV indicates the `uv` tool is not installed or not on PATH.
var ErrNoUV = errors.New("alpha-wrap requires `uv` on PATH: install from https://docs.astral.sh/uv/")

func doWrap(model *loader.LoadedModel, alpha, offset float32) (*loader.LoadedModel, error) {
	if _, err := exec.LookPath("uv"); err != nil {
		return nil, ErrNoUV
	}

	script, err := locateScript()
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "ditherforge-alphawrap-")
	if err != nil {
		return nil, fmt.Errorf("alpha-wrap: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inPath := filepath.Join(tmpDir, "in.stl")
	outPath := filepath.Join(tmpDir, "out.stl")

	if err := writeSTL(inPath, model); err != nil {
		return nil, fmt.Errorf("alpha-wrap: write input STL: %w", err)
	}

	cmd := exec.Command("uv", "run", "--script", script,
		"--in", inPath, "--out", outPath,
		"--alpha", fmt.Sprintf("%g", alpha),
		"--offset", fmt.Sprintf("%g", offset))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		return nil, fmt.Errorf("alpha-wrap: sidecar failed: %w\nstderr: %s\nstdout: %s", err, stderr.String(), string(out))
	}

	wrapped, err := readSTL(outPath)
	if err != nil {
		return nil, fmt.Errorf("alpha-wrap: read output STL: %w", err)
	}
	return wrapped, nil
}

// locateScript returns the absolute path to scripts/alpha_wrap.py. Searches
// the module root (for development runs) and the directory of the running
// executable (for packaged builds alongside the binary).
func locateScript() (string, error) {
	candidates := []string{}
	// Dev: relative to this package's source directory.
	if _, thisFile, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(thisFile), "..", "..", "scripts", "alpha_wrap.py"))
	}
	// Packaged: next to the binary.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts", "alpha_wrap.py"),
			filepath.Join(dir, "alpha_wrap.py"),
			filepath.Join(dir, "..", "Resources", "scripts", "alpha_wrap.py"), // macOS bundle
		)
	}
	// CWD fallback.
	candidates = append(candidates, filepath.Join("scripts", "alpha_wrap.py"))

	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("alpha-wrap: could not locate scripts/alpha_wrap.py (searched %d locations)", len(candidates))
}

func writeSTL(path string, model *loader.LoadedModel) error {
	solid := &stl.Solid{}
	solid.SetBinaryHeader(make([]byte, 80))
	solid.Triangles = make([]stl.Triangle, 0, len(model.Faces))
	for _, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		solid.Triangles = append(solid.Triangles, stl.Triangle{
			Vertices: [3]stl.Vec3{
				{v0[0], v0[1], v0[2]},
				{v1[0], v1[1], v1[2]},
				{v2[0], v2[1], v2[2]},
			},
		})
	}
	return solid.WriteFile(path)
}

func readSTL(path string) (*loader.LoadedModel, error) {
	solid, err := stl.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n := len(solid.Triangles)
	if n == 0 {
		return nil, fmt.Errorf("sidecar produced empty STL")
	}

	// Dedup vertices by snapped position.
	const snap = 1e5
	type key [3]int64
	idx := make(map[key]uint32, n*3)
	verts := make([][3]float32, 0, n*3)
	faces := make([][3]uint32, 0, n)
	for _, tri := range solid.Triangles {
		var face [3]uint32
		for j := range 3 {
			p := tri.Vertices[j]
			k := key{int64(p[0] * snap), int64(p[1] * snap), int64(p[2] * snap)}
			vi, ok := idx[k]
			if !ok {
				vi = uint32(len(verts))
				idx[k] = vi
				verts = append(verts, [3]float32{p[0], p[1], p[2]})
			}
			face[j] = vi
		}
		if face[0] == face[1] || face[1] == face[2] || face[0] == face[2] {
			continue // degenerate after dedup
		}
		faces = append(faces, face)
	}

	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, nil
}
