// Package export3mf writes a 3MF file in BambuStudio/OrcaSlicer format.
package export3mf

import (
	"archive/zip"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/plog"
)

// MaxFilaments is the maximum number of palette colors supported by 3MF export.
const MaxFilaments = 16

// paint_color lookup table from OrcaSlicer/BambuStudio source (Model.cpp CONST_FILAMENTS).
// Index 0 = no filament, index N = filament N (1-based).
var paintColors = []string{
	"", "4", "8", "0C", "1C", "2C", "3C", "4C",
	"5C", "6C", "7C", "8C", "9C", "AC", "BC", "CC", "DC",
}

func paintColor(paletteIndex int) string {
	filament := paletteIndex + 1
	if filament < 0 || filament >= len(paintColors) {
		return ""
	}
	return paintColors[filament]
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/><Default Extension="png" ContentType="image/png"/></Types>`

const rels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/3dmodel.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/><Relationship Target="/Auxiliaries/.thumbnails/thumbnail_3mf.png" Id="rel-2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail"/></Relationships>`

const thumbnailPath = "Auxiliaries/.thumbnails/thumbnail_3mf.png"
const thumbnailSize = 512

// Options configures which printer profile to embed in the 3MF.
type Options struct {
	PrinterID      string  // e.g. "snapmaker_u1"; empty = DefaultPrinterID
	NozzleDiameter float32 // e.g. 0.4; 0 = printer's default nozzle
	LayerHeight    float32 // matched to closest available process profile
	// AppVersion is the ditherforge semver (e.g. "0.6.3"), used to build the
	// BambuStudio-<semver>+ditherforge Application tag that makes Bambu
	// Studio/OrcaSlicer accept the file as a native project.
	AppVersion string
}

// Export writes a single mesh with per-face palette assignments to a 3MF file.
func Export(model *loader.LoadedModel, assignments []int32, outputPath string, paletteRGB [][3]uint8, opts Options) error {
	printerID := opts.PrinterID
	if printerID == "" {
		printerID = DefaultPrinterID
	}
	printer := FindPrinter(printerID)
	if printer == nil {
		return fmt.Errorf("unknown printer %q", printerID)
	}
	var nozzle *Nozzle
	if opts.NozzleDiameter == 0 {
		nozzle = printer.FindNozzle(DefaultNozzleForPrinter(printer))
	} else {
		nozzle = printer.FindNozzleByDiameter(opts.NozzleDiameter)
	}
	if nozzle == nil {
		return fmt.Errorf("printer %q has no %.2fmm nozzle", printerID, opts.NozzleDiameter)
	}
	if len(nozzle.Processes) == 0 {
		return fmt.Errorf("printer %q nozzle %s has no process profiles available", printerID, nozzle.Diameter)
	}
	machineProfile, err := nozzle.loadMachineProfile(printer.ID)
	if err != nil {
		return err
	}
	if printer.IsBambu {
		return exportBambu(model, assignments, outputPath, paletteRGB, printer, nozzle, machineProfile, opts)
	}
	plateX, plateY, err := bedCenter(machineProfile)
	if err != nil {
		return fmt.Errorf("%s: %w", printer.ID, err)
	}

	// Single global tx/ty/tz centres the laid-out model on the bed.
	// In the Split case, both halves are already laid out side-by-side
	// in bed coords; one global translation centers the whole assembly.
	minX, maxX, minY, maxY, minZ := meshExtents(model)
	tx := plateX - float64(minX+maxX)/2
	ty := plateY - float64(minY+maxY)/2
	tz := -float64(minZ)
	transform := fmt.Sprintf("1 0 0 0 1 0 0 0 1 %.4f %.4f %.4f", tx, ty, tz)

	// Build a uniform list of parts. Single-mesh exports have one
	// part (the whole model); Split exports have one part per
	// FaceMeshIdx group. Each part becomes a top-level <object>
	// at id 2+i with a build-item placement.
	var parts []*part
	if mp := splitModelByMesh(model, assignments); mp != nil {
		for i, p := range mp {
			plog.Printf("  Export 3MF: part %d — %d verts, %d faces", i, len(p.Vertices), len(p.Faces))
			parts = append(parts, &part{
				objUUID:   newUUID(),
				instUUID:  newUUID(),
				compUUID:  newUUID(),
				objectID:  2 + i,
				innerPath: fmt.Sprintf("/3D/Objects/object_%d.model", i+1),
				innerRel:  fmt.Sprintf("rel-%d", i+1),
				verts:     p.Vertices,
				faces:     p.Faces,
				assigns:   p.Assignments,
			})
		}
	} else {
		parts = []*part{{
			objUUID:   newUUID(),
			instUUID:  newUUID(),
			compUUID:  newUUID(),
			objectID:  2,
			innerPath: "/3D/Objects/object_1.model",
			innerRel:  "rel-1",
			verts:     model.Vertices,
			faces:     model.Faces,
			assigns:   assignments,
		}}
	}

	buildUUID := newUUID()

	// objectRels lists every inner .model file as a relationship.
	var orelsB strings.Builder
	orelsB.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, p := range parts {
		fmt.Fprintf(&orelsB, `<Relationship Target="%s" Id="%s" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>`, p.innerPath, p.innerRel)
	}
	orelsB.WriteString(`</Relationships>`)
	objectRels := orelsB.String()

	// Attribute ditherforge via standard 3MF metadata. We intentionally do NOT
	// prefix Application with "BambuStudio-" / "OrcaSlicer-": doing so sets
	// m_is_bbl_3mf in Bambu Studio's importer, which then strictly parses
	// project_settings.config and expects plate/slice files we don't emit.
	appVersion := opts.AppVersion
	if appVersion == "" {
		appVersion = "0.0.0"
	}
	applicationTag := fmt.Sprintf("ditherforge-%s", appVersion)

	var mb strings.Builder
	mb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	mb.WriteString(`<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" unit="millimeter" xml:lang="en-US" requiredextensions="p">`)
	fmt.Fprintf(&mb, `<metadata name="Application">%s</metadata>`, applicationTag)
	mb.WriteString(`<metadata name="Designer">ditherforge</metadata>`)
	mb.WriteString(`<metadata name="Title">ditherforge output</metadata>`)
	mb.WriteString(`<resources>`)
	for _, p := range parts {
		fmt.Fprintf(&mb, `<object id="%d" p:UUID="%s" type="model">`, p.objectID, p.objUUID)
		fmt.Fprintf(&mb, `<components><component p:path="%s" objectid="1" p:UUID="%s" transform="%s"/></components>`, p.innerPath, p.compUUID, transform)
		mb.WriteString(`</object>`)
	}
	mb.WriteString(`</resources>`)
	fmt.Fprintf(&mb, `<build p:UUID="%s">`, buildUUID)
	for _, p := range parts {
		fmt.Fprintf(&mb, `<item objectid="%d" p:UUID="%s" transform="1 0 0 0 1 0 0 0 1 0 0 0" printable="1"/>`, p.objectID, p.instUUID)
	}
	mb.WriteString(`</build></model>`)
	mainModel := mb.String()

	modelSettings := buildModelSettingsParts(parts, len(model.Faces))

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	writeEntry := func(name, content string) error {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:   name,
			Method: zip.Deflate,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(w, content)
		return err
	}
	writeBinary := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:   name,
			Method: zip.Deflate,
		})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	if err := writeEntry("[Content_Types].xml", contentTypes); err != nil {
		return err
	}
	if err := writeEntry("_rels/.rels", rels); err != nil {
		return err
	}
	if err := writeEntry("3D/3dmodel.model", mainModel); err != nil {
		return err
	}
	if err := writeEntry("3D/_rels/3dmodel.model.rels", objectRels); err != nil {
		return err
	}
	for _, p := range parts {
		// Strip the leading "/" so the zip entry path matches the
		// 3MF convention.
		entryName := p.innerPath
		if len(entryName) > 0 && entryName[0] == '/' {
			entryName = entryName[1:]
		}
		partModel := &loader.LoadedModel{Vertices: p.verts, Faces: p.faces}
		if err := writeEntry(entryName, buildObjectModel(partModel, p.assigns, newUUID())); err != nil {
			return err
		}
	}
	if err := writeEntry("Metadata/model_settings.config", modelSettings); err != nil {
		return err
	}
	if paletteRGB != nil {
		ps, err := buildProjectSettings(printer, nozzle, machineProfile, paletteRGB, opts.LayerHeight)
		if err != nil {
			return err
		}
		if err := writeEntry("Metadata/project_settings.config", ps); err != nil {
			return err
		}
	}
	if paletteRGB != nil && len(assignments) == len(model.Faces) {
		thumb, err := renderThumbnail(model, assignments, paletteRGB, thumbnailSize)
		if err != nil {
			return fmt.Errorf("render thumbnail: %w", err)
		}
		if err := writeBinary(thumbnailPath, thumb); err != nil {
			return err
		}
	}

	return nil
}

// part is one top-level <object> in the 3MF output. Single-mesh
// exports have one part; Split-aware multi-part exports have one per
// FaceMeshIdx group. The fields capture the UUID + ID + path
// scaffolding plus the geometry/assignments slices.
type part struct {
	objUUID   string
	instUUID  string
	compUUID  string
	objectID  int
	innerPath string
	innerRel  string
	verts     [][3]float32
	faces     [][3]uint32
	assigns   []int32
}

// splitPart is one mesh extracted from a multi-mesh LoadedModel via
// FaceMeshIdx. Each part has a self-contained vertex table (only
// vertices referenced by the part's faces) with remapped face
// indices. Used by the Split-aware export path to emit one
// `<object>` entry per FaceMeshIdx group.
type splitPart struct {
	Vertices    [][3]float32
	Faces       [][3]uint32
	Assignments []int32
}

// splitModelByMesh partitions a LoadedModel into per-FaceMeshIdx
// parts, with each part's vertex table compacted to only the
// vertices its faces reference. Returns nil for single-mesh models
// (NumMeshes <= 1) so the caller can take the unchanged
// single-object export path.
func splitModelByMesh(model *loader.LoadedModel, assignments []int32) []*splitPart {
	if model.NumMeshes <= 1 || len(model.FaceMeshIdx) != len(model.Faces) {
		return nil
	}
	parts := make([]*splitPart, model.NumMeshes)
	for i := range parts {
		parts[i] = &splitPart{}
	}
	// Per-part: source-vertex-index → part-local index.
	vertMap := make([]map[uint32]uint32, model.NumMeshes)
	for i := range vertMap {
		vertMap[i] = make(map[uint32]uint32)
	}
	for fi, f := range model.Faces {
		m := int(model.FaceMeshIdx[fi])
		if m < 0 || m >= model.NumMeshes {
			continue
		}
		var newF [3]uint32
		for k, vi := range f {
			localIdx, ok := vertMap[m][vi]
			if !ok {
				localIdx = uint32(len(parts[m].Vertices))
				parts[m].Vertices = append(parts[m].Vertices, model.Vertices[vi])
				vertMap[m][vi] = localIdx
			}
			newF[k] = localIdx
		}
		parts[m].Faces = append(parts[m].Faces, newF)
		if assignments != nil && fi < len(assignments) {
			parts[m].Assignments = append(parts[m].Assignments, assignments[fi])
		}
	}
	return parts
}

// buildObjectModel writes the inner /3D/Objects/object_N.model with vertices,
// triangles, and paint_color assignments. Shared by the generic and Bambu
// export paths; they differ only in how objUUID is sourced.
func buildObjectModel(model *loader.LoadedModel, assignments []int32, objUUID string) string {
	var sb strings.Builder

	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<model unit="millimeter" xml:lang="en-US"`)
	sb.WriteString(` xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"`)
	sb.WriteString(` xmlns:BambuStudio="http://schemas.bambulab.com/package/2021"`)
	sb.WriteString(` xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"`)
	sb.WriteString(` requiredextensions="p">`)
	sb.WriteString(`<metadata name="BambuStudio:3mfVersion">1</metadata>`)
	sb.WriteString(`<resources>`)
	fmt.Fprintf(&sb, `<object id="1" p:UUID="%s" type="model">`, objUUID)
	sb.WriteString(`<mesh><vertices>`)

	for _, v := range model.Vertices {
		fmt.Fprintf(&sb, `<vertex x="%.6f" y="%.6f" z="%.6f"/>`, v[0], v[1], v[2])
	}
	sb.WriteString(`</vertices>`)

	// Snap vertices to export precision for degenerate detection.
	type snapV struct{ x, y, z int32 }
	snapped := make([]snapV, len(model.Vertices))
	for i, v := range model.Vertices {
		snapped[i] = snapV{
			int32(math.Round(float64(v[0]) * 1e6)),
			int32(math.Round(float64(v[1]) * 1e6)),
			int32(math.Round(float64(v[2]) * 1e6)),
		}
	}

	sb.WriteString(`<triangles>`)
	for fi, face := range model.Faces {
		// Skip degenerate triangles (vertices identical at export precision).
		s0, s1, s2 := snapped[face[0]], snapped[face[1]], snapped[face[2]]
		if s0 == s1 || s1 == s2 || s0 == s2 {
			continue
		}
		pc := paintColor(int(assignments[fi]))
		fmt.Fprintf(&sb, `<triangle v1="%d" v2="%d" v3="%d" paint_color="%s"/>`,
			face[0], face[1], face[2], pc)
	}
	sb.WriteString(`</triangles>`)

	sb.WriteString(`</mesh></object></resources><build/></model>`)

	return sb.String()
}

func buildProjectSettings(printer *Printer, nozzle *Nozzle, machineProfile map[string]any, paletteRGB [][3]uint8, layerHeight float32) (string, error) {
	// machineProfile is freshly unmarshalled per call by loadMachineProfile,
	// so top-level assignments here are safe without a deep copy.
	data := machineProfile

	// Strip the "name" field (it's the machine profile name; we set
	// printer_settings_id explicitly below and name the project separately).
	delete(data, "name")

	// Merge the closest process profile by layer height.
	var processName string
	if pp := nozzle.ClosestProcess(layerHeight); pp != nil {
		processData, err := loadProcessProfile(printer.ID, pp)
		if err != nil {
			return "", err
		}
		if n, _ := processData["name"].(string); n != "" {
			processName = n
		}
		delete(processData, "name")
		for k, v := range processData {
			data[k] = v
		}
	}

	data["name"] = "project_settings"
	if processName != "" {
		data["print_settings_id"] = processName
	}
	data["printer_settings_id"] = nozzle.PrinterSettingsID
	data["printer_technology"] = "FFF"

	// Filament info.
	hexColors := make([]string, len(paletteRGB))
	for i, p := range paletteRGB {
		hexColors[i] = fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
	}
	filamentTypes := make([]string, len(paletteRGB))
	for i := range filamentTypes {
		filamentTypes[i] = "Generic PLA"
	}
	filamentIDs := make([]string, len(paletteRGB))
	for i := range filamentIDs {
		filamentIDs[i] = "Generic PLA"
	}
	data["filament_colour"] = hexColors
	data["filament_type"] = filamentTypes
	data["filament_settings_id"] = filamentIDs
	// Limit painted color depth from surface to 1.5mm so the slicer doesn't
	// flood-fill entire regions with a single filament.
	data["mmu_segmented_region_max_width"] = "1.5"

	// Tell OrcaSlicer which settings differ from the system defaults.
	// Without this, OrcaSlicer ignores customized values. The first element
	// lists print settings (semicolon-separated); remaining elements are for
	// per-filament and printer overrides (currently empty).
	nFilaments := len(paletteRGB)
	diffSettings := make([]string, 2+nFilaments) // print + filaments + printer
	diffSettings[0] = "mmu_segmented_region_max_width"
	data["different_settings_to_system"] = diffSettings

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildModelSettingsParts(parts []*part, totalFaces int) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<config>`)
	for i, p := range parts {
		fmt.Fprintf(&sb, `<object id="%d">`, p.objectID)
		// Name distinguishes halves so the slicer's UI shows them
		// separately. Single-mesh exports stay "ditherforge_output".
		name := "ditherforge_output"
		if len(parts) > 1 {
			name = fmt.Sprintf("ditherforge_output_part%d", i+1)
		}
		fmt.Fprintf(&sb, `<metadata key="name" value="%s"/>`, name)
		sb.WriteString(`<metadata key="extruder" value="1"/>`)
		fmt.Fprintf(&sb, `<metadata face_count="%d"/>`, len(p.faces))
		sb.WriteString(`<part id="1" subtype="normal_part">`)
		sb.WriteString(`<metadata key="name" value="shell"/>`)
		sb.WriteString(`<metadata key="extruder" value="1"/>`)
		sb.WriteString(`</part></object>`)
	}
	sb.WriteString(`</config>`)
	_ = totalFaces
	return sb.String()
}
