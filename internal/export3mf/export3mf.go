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

const contentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/></Types>`

const rels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/3dmodel.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`

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
	plateX, plateY, err := bedCenter(machineProfile)
	if err != nil {
		return fmt.Errorf("%s: %w", printer.ID, err)
	}

	objUUID := newUUID()
	instUUID := newUUID()
	buildUUID := newUUID()

	var minX, maxX, minY, maxY, minZ float32
	if len(model.Vertices) > 0 {
		verts := model.Vertices
		minX, maxX = verts[0][0], verts[0][0]
		minY, maxY = verts[0][1], verts[0][1]
		minZ = verts[0][2]
		for _, v := range verts[1:] {
			if v[0] < minX {
				minX = v[0]
			}
			if v[0] > maxX {
				maxX = v[0]
			}
			if v[1] < minY {
				minY = v[1]
			}
			if v[1] > maxY {
				maxY = v[1]
			}
			if v[2] < minZ {
				minZ = v[2]
			}
		}
	}
	tx := plateX - float64(minX+maxX)/2
	ty := plateY - float64(minY+maxY)/2
	tz := -float64(minZ)
	transform := fmt.Sprintf("1 0 0 0 1 0 0 0 1 %.4f %.4f %.4f", tx, ty, tz)

	objectRels := `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/Objects/object_1.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`

	// Attribute ditherforge via standard 3MF metadata. We intentionally do NOT
	// prefix Application with "BambuStudio-" / "OrcaSlicer-": doing so sets
	// m_is_bbl_3mf in Bambu Studio's importer, which then strictly parses
	// project_settings.config and expects plate/slice files we don't emit.
	appVersion := opts.AppVersion
	if appVersion == "" {
		appVersion = "0.0.0"
	}
	applicationTag := fmt.Sprintf("ditherforge-%s", appVersion)

	mainModel := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+
		`<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" unit="millimeter" xml:lang="en-US" requiredextensions="p">`+
		`<metadata name="Application">%s</metadata>`+
		`<metadata name="Designer">ditherforge</metadata>`+
		`<metadata name="Title">ditherforge output</metadata>`+
		`<resources><object id="2" p:UUID="%s" type="model">`+
		`<components><component p:path="/3D/Objects/object_1.model" objectid="1" p:UUID="%s" transform="%s"/></components>`+
		`</object></resources>`+
		`<build p:UUID="%s"><item objectid="2" p:UUID="%s" transform="1 0 0 0 1 0 0 0 1 0 0 0" printable="1"/></build>`+
		`</model>`, applicationTag, objUUID, newUUID(), transform, buildUUID, instUUID)

	modelSettings := buildModelSettings(model)

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
	objectModel, err := buildObjectModel(model, assignments)
	if err != nil {
		return err
	}
	if err := writeEntry("3D/Objects/object_1.model", objectModel); err != nil {
		return err
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

	return nil
}

func buildObjectModel(model *loader.LoadedModel, assignments []int32) (string, error) {
	objUUID := newUUID()
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

	return sb.String(), nil
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

func buildModelSettings(model *loader.LoadedModel) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<config><object id="2">`)
	sb.WriteString(`<metadata key="name" value="ditherforge_output"/>`)
	sb.WriteString(`<metadata key="extruder" value="1"/>`)
	fmt.Fprintf(&sb, `<metadata face_count="%d"/>`, len(model.Faces))
	sb.WriteString(`<part id="1" subtype="normal_part">`)
	sb.WriteString(`<metadata key="name" value="shell"/>`)
	sb.WriteString(`<metadata key="extruder" value="1"/>`)
	sb.WriteString(`</part></object></config>`)
	return sb.String()
}
