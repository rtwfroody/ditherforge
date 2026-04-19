// Printer registry backed by flattened OrcaSlicer profiles embedded from
// internal/export3mf/profiles/. See devscripts/flatten_orca_profiles.py for
// how the embedded files are generated.
package export3mf

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed profiles
var profilesFS embed.FS

// ProcessProfile is a single layer-height slicing profile for a nozzle.
type ProcessProfile struct {
	LayerHeight float32 `json:"layer_height"`
	Name        string  `json:"name"`
	File        string  `json:"file"`
}

// Nozzle is a single nozzle variant of a printer.
type Nozzle struct {
	Diameter          string           `json:"diameter"` // e.g. "0.4"
	PrinterSettingsID string           `json:"printer_settings_id"`
	MachineFile       string           `json:"machine_file"`
	Processes         []ProcessProfile `json:"processes"`
}

// Printer is a supported printer model.
type Printer struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Nozzles     []Nozzle `json:"nozzles"`
}

// manifest is the root object in profiles/manifest.json.
type manifest struct {
	Printers []Printer `json:"printers"`
}

var (
	registryOnce sync.Once
	registryAll  []Printer
	registryByID map[string]*Printer
	registryErr  error
)

func loadRegistry() {
	registryOnce.Do(func() {
		data, err := profilesFS.ReadFile("profiles/manifest.json")
		if err != nil {
			registryErr = fmt.Errorf("read profile manifest: %w", err)
			return
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil {
			registryErr = fmt.Errorf("parse profile manifest: %w", err)
			return
		}
		registryAll = m.Printers
		registryByID = make(map[string]*Printer, len(m.Printers))
		for i := range registryAll {
			registryByID[registryAll[i].ID] = &registryAll[i]
		}
	})
}

// Registry returns all supported printers, in manifest order.
func Registry() ([]Printer, error) {
	loadRegistry()
	if registryErr != nil {
		return nil, registryErr
	}
	return registryAll, nil
}

// FindPrinter returns the printer with the given ID, or nil if not found.
func FindPrinter(id string) *Printer {
	loadRegistry()
	if registryErr != nil {
		return nil
	}
	return registryByID[id]
}

// FindNozzle returns the nozzle variant with the given diameter string
// (e.g. "0.4"), or nil if this printer doesn't have that nozzle.
func (p *Printer) FindNozzle(diameter string) *Nozzle {
	for i := range p.Nozzles {
		if p.Nozzles[i].Diameter == diameter {
			return &p.Nozzles[i]
		}
	}
	return nil
}

// FindNozzleByDiameter returns the nozzle whose parsed diameter equals the
// given float (within 0.001mm tolerance), or nil if no variant matches.
// Used when callers hold a float rather than a canonical string.
func (p *Printer) FindNozzleByDiameter(diameter float32) *Nozzle {
	for i := range p.Nozzles {
		d, err := strconv.ParseFloat(p.Nozzles[i].Diameter, 32)
		if err != nil {
			continue
		}
		if math.Abs(d-float64(diameter)) < 0.001 {
			return &p.Nozzles[i]
		}
	}
	return nil
}

// ClosestProcess returns the process profile whose layer height is closest
// to the requested value, or nil if the nozzle has no process profiles.
func (n *Nozzle) ClosestProcess(layerHeight float32) *ProcessProfile {
	if len(n.Processes) == 0 {
		return nil
	}
	best := 0
	bestD := float32(math.Inf(1))
	for i, pp := range n.Processes {
		d := pp.LayerHeight - layerHeight
		if d < 0 {
			d = -d
		}
		if d < bestD {
			bestD = d
			best = i
		}
	}
	return &n.Processes[best]
}

// loadMachineProfile reads and parses this nozzle's flattened machine profile.
func (n *Nozzle) loadMachineProfile(printerID string) (map[string]any, error) {
	p := path.Join("profiles", printerID, n.MachineFile)
	data, err := profilesFS.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read machine profile %q: %w", p, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse machine profile %q: %w", p, err)
	}
	return out, nil
}

// loadProcessProfile reads and parses a process profile.
func loadProcessProfile(printerID string, pp *ProcessProfile) (map[string]any, error) {
	p := path.Join("profiles", printerID, pp.File)
	data, err := profilesFS.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read process profile %q: %w", p, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse process profile %q: %w", p, err)
	}
	return out, nil
}

// bedCenter returns the X,Y center of the build plate's printable area,
// parsed from the flattened machine profile's "printable_area" field.
// printable_area is a list of "X x Y" points describing the polygon corners.
func bedCenter(profile map[string]any) (float64, float64, error) {
	raw, ok := profile["printable_area"]
	if !ok {
		return 0, 0, fmt.Errorf("profile missing printable_area")
	}
	pts, ok := raw.([]any)
	if !ok || len(pts) == 0 {
		return 0, 0, fmt.Errorf("printable_area not a non-empty list")
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for i, p := range pts {
		s, ok := p.(string)
		if !ok {
			return 0, 0, fmt.Errorf("printable_area[%d] is not a string", i)
		}
		// Points are "Xx Y" or "XxY" with a lowercase 'x' separator.
		parts := strings.SplitN(strings.ToLower(s), "x", 2)
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("printable_area[%d] = %q: expected \"XxY\"", i, s)
		}
		x, errX := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		y, errY := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if errX != nil || errY != nil {
			return 0, 0, fmt.Errorf("printable_area[%d] = %q: unparseable coordinates", i, s)
		}
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		if y < minY {
			minY = y
		}
		if y > maxY {
			maxY = y
		}
	}
	if math.IsInf(minX, 0) {
		return 0, 0, fmt.Errorf("printable_area had no points")
	}
	return (minX + maxX) / 2, (minY + maxY) / 2, nil
}

// DefaultPrinterID is the printer selected when none is specified.
const DefaultPrinterID = "snapmaker_u1"

// DefaultNozzleForPrinter returns the 0.4mm nozzle if available, else the
// first listed nozzle.
func DefaultNozzleForPrinter(p *Printer) string {
	if p == nil || len(p.Nozzles) == 0 {
		return ""
	}
	for _, n := range p.Nozzles {
		if n.Diameter == "0.4" {
			return n.Diameter
		}
	}
	return p.Nozzles[0].Diameter
}

// DefaultLayerHeightForNozzle returns a sensible default layer height for
// a nozzle: 0.20 if available, else the closest to 0.20, else the first.
func DefaultLayerHeightForNozzle(n *Nozzle) float32 {
	if n == nil || len(n.Processes) == 0 {
		return 0
	}
	p := n.ClosestProcess(0.20)
	return p.LayerHeight
}

