package export3mf

import (
	"archive/zip"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestRegistryProfilesComplete validates every embedded printer profile:
// each printer has at least one nozzle, each nozzle's machine profile
// loads and yields a bed center, and each process profile loads and
// yields a usable shell thickness. Guards against a bad regeneration by
// scripts/flatten_orca_profiles.py (missing files, renamed profiles,
// unparseable settings).
func TestRegistryProfilesComplete(t *testing.T) {
	printers, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	if len(printers) == 0 {
		t.Fatal("registry is empty")
	}
	for _, p := range printers {
		if len(p.Nozzles) == 0 {
			t.Errorf("%s: no nozzles", p.ID)
			continue
		}
		for i := range p.Nozzles {
			n := &p.Nozzles[i]
			machine, err := n.loadMachineProfile(p.ID)
			if err != nil {
				t.Errorf("%s nozzle %s: %v", p.ID, n.Diameter, err)
				continue
			}
			if _, _, err := bedCenter(machine); err != nil {
				t.Errorf("%s nozzle %s: bedCenter: %v", p.ID, n.Diameter, err)
			}
			if len(n.Processes) == 0 {
				t.Errorf("%s nozzle %s: no processes", p.ID, n.Diameter)
			}
			for j := range n.Processes {
				pp := &n.Processes[j]
				if _, err := loadProcessProfile(p.ID, pp); err != nil {
					t.Errorf("%s nozzle %s: %v", p.ID, n.Diameter, err)
					continue
				}
				if _, ok := ShellThicknessMM(p.ID, n, pp); !ok {
					t.Errorf("%s nozzle %s process %s: no shell thickness",
						p.ID, n.Diameter, pp.Name)
				}
				if pp.LayerHeight <= 0 {
					t.Errorf("%s nozzle %s process %s: layer height %v",
						p.ID, n.Diameter, pp.Name, pp.LayerHeight)
				}
			}
			if p.IsBambu {
				if _, err := n.loadFilamentProfile(p.ID); err != nil {
					t.Errorf("%s nozzle %s: %v", p.ID, n.Diameter, err)
				}
			}
		}
	}
}

// TestExportAllPrinters smoke-tests the full Export path for every
// registered printer at its default nozzle: a tiny tetrahedron with a
// 2-color palette must produce a readable 3MF archive containing the
// model part.
func TestExportAllPrinters(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {10, 0, 0}, {0, 10, 0}, {0, 0, 10},
		},
		Faces:     [][3]uint32{{0, 2, 1}, {0, 1, 3}, {0, 3, 2}, {1, 2, 3}},
		NumMeshes: 1,
	}
	assignments := []int32{0, 1, 0, 1}
	palette := [][3]uint8{{255, 0, 0}, {0, 0, 255}}

	printers, err := Registry()
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	for _, p := range printers {
		t.Run(p.ID, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out.3mf")
			opts := Options{PrinterID: p.ID, AppVersion: "0.0.0-test"}
			if err := Export(model, assignments, out, palette, opts); err != nil {
				t.Fatalf("Export: %v", err)
			}
			zr, err := zip.OpenReader(out)
			if err != nil {
				t.Fatalf("not a readable zip: %v", err)
			}
			defer zr.Close()
			found := false
			for _, f := range zr.File {
				if f.Name == "3D/3dmodel.model" {
					found = true
				}
			}
			if !found {
				t.Error("3MF missing 3D/3dmodel.model")
			}
		})
	}
}
