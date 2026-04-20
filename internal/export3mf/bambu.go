package export3mf

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// BBL-project 3MF writer. See:
// https://github.com/SoftFever/OrcaSlicer/blob/main/src/libslic3r/PresetBundle.cpp
// Bambu Studio detects a BBL project via the <metadata name="Application">
// prefix ("BambuStudio-" or "OrcaSlicer-"). Once detected, it strictly
// validates project_settings.config against its PresetBundle expectations, in
// particular that filament_* arrays are sized to num_filaments × num_variants
// and that filament_self_index is self-consistent with filament_extruder_variant.

// bambuClientVersion is the Bambu/Orca client version we claim to be. Chosen
// to match recent public releases (OrcaSlicer/BambuStudio 2.6.0.x range); it
// only needs to look like a real slicer version for the BBL loader.
const bambuClientVersion = "02.06.00.51"

// Bambu uses deterministic UUID suffixes so its importer can recognise the
// object/build/sub-object hierarchy. See Model.cpp OBJECT_UUID_SUFFIX etc.
const (
	bambuObjectUUIDSuffix    = "-61cb-4c03-9d28-80fed5dfa1dc"
	bambuBuildUUIDSuffix     = "-b1ec-4553-aec9-835e5b724bb4"
	bambuSubObjectUUIDSuffix = "-81cb-4c03-9d28-80fed5dfa1dc"
)

// exportBambu writes a full BBL-project 3MF bundle (machine + process +
// filament profiles merged and expanded per num_filaments, BBL metadata,
// thumbnails, plate/sequence stubs). Only called when the target printer is
// a Bambu model.
func exportBambu(
	model *loader.LoadedModel,
	assignments []int32,
	outputPath string,
	paletteRGB [][3]uint8,
	printer *Printer,
	nozzle *Nozzle,
	machineProfile map[string]any,
	opts Options,
) error {
	if len(paletteRGB) == 0 {
		return fmt.Errorf("bambu export requires a non-empty palette")
	}

	filamentProfile, err := nozzle.loadFilamentProfile(printer.ID)
	if err != nil {
		return err
	}

	variants := extractVariants(machineProfile)

	plateX, plateY, err := bedCenter(machineProfile)
	if err != nil {
		return fmt.Errorf("%s: %w", printer.ID, err)
	}

	minX, maxX, minY, maxY, minZ := meshExtents(model)
	tx := plateX - float64(minX+maxX)/2
	ty := plateY - float64(minY+maxY)/2
	tz := -float64(minZ)
	buildTransform := fmt.Sprintf("1 0 0 0 1 0 0 0 1 %.6f %.6f %.6f", tx, ty, tz)

	// UUID scheme matches BambuStudio exports: object/item UUIDs are
	// "<counter>-<suffix>" using the suffix pool for that resource; build
	// UUID is random.
	//
	// componentID uses a distinct suffix (not the sub-object pool) to match
	// what BambuStudio emits. Its importer wires the outer <component> to
	// the inner <object id="1"> via objectid + p:path, not by UUID, so the
	// values can differ. Keeping the counter at 0x10000 mirrors the
	// reference so the file looks round-tripped through BambuStudio.
	objID := newBambuUUID(1, bambuObjectUUIDSuffix)
	subObjID := newBambuUUID(1, bambuSubObjectUUIDSuffix)
	componentID := newBambuUUID(0x10000, "-b206-40ff-9872-83e8017abed1")
	buildID := newUUID()
	buildItemID := newBambuUUID(2, bambuBuildUUIDSuffix)

	appVersion := opts.AppVersion
	if appVersion == "" {
		appVersion = "0.0.0"
	}
	applicationTag := fmt.Sprintf("BambuStudio-%s+ditherforge-%s", bambuClientVersion, appVersion)
	creationDate := time.Now().UTC().Format("2006-01-02")

	mainModel := buildBambuMainModel(applicationTag, creationDate, objID, buildID, componentID, buildItemID, buildTransform)
	objectModel := buildObjectModel(model, assignments, subObjID)
	modelSettings := buildBambuModelSettings(model)
	projectSettings, err := buildBambuProjectSettings(printer, nozzle, machineProfile, filamentProfile, paletteRGB, opts.LayerHeight, variants)
	if err != nil {
		return err
	}
	plateJSON := buildBambuPlateJSON(model, nozzle)
	filamentSeq := `{"plate_1":{"nozzle_sequence":[],"optimal_assignment":[],"sequence":[]}}`
	sliceInfo := buildBambuSliceInfo()

	thumb, err := renderThumbnail(model, assignments, paletteRGB, thumbnailSize)
	if err != nil {
		return fmt.Errorf("render thumbnail: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	writeText := func(name, content string) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(w, content)
		return err
	}
	writeBin := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	entries := []struct {
		name, body string
	}{
		{"[Content_Types].xml", bambuContentTypes},
		{"_rels/.rels", bambuRels},
		{"3D/3dmodel.model", mainModel},
		{"3D/_rels/3dmodel.model.rels", bambuObjectRels},
		{"3D/Objects/object_1.model", objectModel},
		{"Metadata/model_settings.config", modelSettings},
		{"Metadata/project_settings.config", projectSettings},
		{"Metadata/slice_info.config", sliceInfo},
		{"Metadata/plate_1.json", plateJSON},
		{"Metadata/filament_sequence.json", filamentSeq},
	}
	for _, e := range entries {
		if err := writeText(e.name, e.body); err != nil {
			return err
		}
	}

	for _, p := range []string{
		"Auxiliaries/.thumbnails/thumbnail_3mf.png",
		"Auxiliaries/.thumbnails/thumbnail_middle.png",
		"Auxiliaries/.thumbnails/thumbnail_small.png",
		"Metadata/plate_1.png",
		"Metadata/plate_1_small.png",
		"Metadata/plate_no_light_1.png",
		"Metadata/top_1.png",
		"Metadata/pick_1.png",
	} {
		if err := writeBin(p, thumb); err != nil {
			return err
		}
	}

	return nil
}

// diameterFloat returns the nozzle's parsed diameter as a float64, or 0.4 if
// parsing fails (shouldn't happen for manifest-driven values).
func (n *Nozzle) diameterFloat() float64 {
	d, err := strconv.ParseFloat(n.Diameter, 64)
	if err != nil {
		return 0.4
	}
	return d
}

// newBambuUUID returns a UUID whose prefix encodes index and whose suffix
// matches Bambu's deterministic tag. Bambu's importer only checks suffix.
func newBambuUUID(index int, suffix string) string {
	return fmt.Sprintf("%08x%s", index, suffix)
}

// meshExtents returns the axis-aligned bounding box of the model.
func meshExtents(model *loader.LoadedModel) (minX, maxX, minY, maxY, minZ float32) {
	if len(model.Vertices) == 0 {
		return
	}
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
	return
}

const bambuContentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/>` +
	`<Default Extension="png" ContentType="image/png"/>` +
	`<Default Extension="gcode" ContentType="text/x.gcode"/>` +
	`</Types>`

const bambuRels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Target="/3D/3dmodel.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>` +
	`<Relationship Target="/Auxiliaries/.thumbnails/thumbnail_3mf.png" Id="rel-2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail"/>` +
	`<Relationship Target="/Auxiliaries/.thumbnails/thumbnail_middle.png" Id="rel-4" Type="http://schemas.bambulab.com/package/2021/cover-thumbnail-middle"/>` +
	`<Relationship Target="/Auxiliaries/.thumbnails/thumbnail_small.png" Id="rel-5" Type="http://schemas.bambulab.com/package/2021/cover-thumbnail-small"/>` +
	`</Relationships>`

const bambuObjectRels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Target="/3D/Objects/object_1.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>` +
	`</Relationships>`

func buildBambuMainModel(appTag, creationDate, objUUID, buildUUID, componentUUID, buildItemUUID, buildTransform string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>`+
		`<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:BambuStudio="http://schemas.bambulab.com/package/2021" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" unit="millimeter" xml:lang="en-US" requiredextensions="p">`+
		`<metadata name="Application">%s</metadata>`+
		`<metadata name="BambuStudio:3mfVersion">1</metadata>`+
		`<metadata name="CreationDate">%s</metadata>`+
		`<metadata name="Designer">ditherforge</metadata>`+
		`<metadata name="ModificationDate">%s</metadata>`+
		`<metadata name="Thumbnail_Middle">/Auxiliaries/.thumbnails/thumbnail_middle.png</metadata>`+
		`<metadata name="Thumbnail_Small">/Auxiliaries/.thumbnails/thumbnail_small.png</metadata>`+
		`<metadata name="Title">ditherforge output</metadata>`+
		`<resources>`+
		`<object id="2" p:UUID="%s" type="model">`+
		`<components>`+
		`<component p:path="/3D/Objects/object_1.model" objectid="1" p:UUID="%s" transform="1 0 0 0 1 0 0 0 1 0 0 0"/>`+
		`</components>`+
		`</object>`+
		`</resources>`+
		`<build p:UUID="%s">`+
		`<item objectid="2" p:UUID="%s" transform="%s" printable="1"/>`+
		`</build>`+
		`</model>`,
		appTag, creationDate, creationDate,
		objUUID, componentUUID,
		buildUUID, buildItemUUID, buildTransform)
}

func buildBambuModelSettings(model *loader.LoadedModel) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString("\n<config>")
	sb.WriteString("\n  <object id=\"2\">")
	sb.WriteString("\n    <metadata key=\"name\" value=\"ditherforge_output\"/>")
	sb.WriteString("\n    <metadata key=\"extruder\" value=\"1\"/>")
	fmt.Fprintf(&sb, "\n    <metadata face_count=\"%d\"/>", len(model.Faces))
	sb.WriteString("\n    <part id=\"1\" subtype=\"normal_part\">")
	sb.WriteString("\n      <metadata key=\"name\" value=\"shell\"/>")
	sb.WriteString("\n      <metadata key=\"matrix\" value=\"1 0 0 0 0 1 0 0 0 0 1 0 0 0 0 1\"/>")
	sb.WriteString("\n      <metadata key=\"source_file\" value=\"ditherforge_output\"/>")
	sb.WriteString("\n      <metadata key=\"source_object_id\" value=\"0\"/>")
	sb.WriteString("\n      <metadata key=\"source_volume_id\" value=\"0\"/>")
	fmt.Fprintf(&sb, "\n      <mesh_stat face_count=\"%d\" edges_fixed=\"0\" degenerate_facets=\"0\" facets_removed=\"0\" facets_reversed=\"0\" backwards_edges=\"0\"/>", len(model.Faces))
	sb.WriteString("\n    </part>")
	sb.WriteString("\n  </object>")
	sb.WriteString("\n  <plate>")
	sb.WriteString("\n    <metadata key=\"plater_id\" value=\"1\"/>")
	sb.WriteString("\n    <metadata key=\"plater_name\" value=\"\"/>")
	sb.WriteString("\n    <metadata key=\"locked\" value=\"false\"/>")
	sb.WriteString("\n    <metadata key=\"filament_map_mode\" value=\"Auto For Flush\"/>")
	sb.WriteString("\n    <metadata key=\"thumbnail_file\" value=\"Metadata/plate_1.png\"/>")
	sb.WriteString("\n    <metadata key=\"thumbnail_no_light_file\" value=\"Metadata/plate_no_light_1.png\"/>")
	sb.WriteString("\n    <metadata key=\"top_file\" value=\"Metadata/top_1.png\"/>")
	sb.WriteString("\n    <metadata key=\"pick_file\" value=\"Metadata/pick_1.png\"/>")
	sb.WriteString("\n    <model_instance>")
	sb.WriteString("\n      <metadata key=\"object_id\" value=\"2\"/>")
	sb.WriteString("\n      <metadata key=\"instance_id\" value=\"0\"/>")
	sb.WriteString("\n      <metadata key=\"identify_id\" value=\"1\"/>")
	sb.WriteString("\n    </model_instance>")
	sb.WriteString("\n  </plate>")
	sb.WriteString("\n  <assemble>")
	sb.WriteString("\n    <assemble_item object_id=\"2\" instance_id=\"0\" transform=\"1 0 0 0 1 0 0 0 1 0 0 0\" offset=\"0 0 0\"/>")
	sb.WriteString("\n  </assemble>")
	sb.WriteString("\n</config>")
	return sb.String()
}

func buildBambuSliceInfo() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<config>
  <header>
    <header_item key="X-BBL-Client-Type" value="slicer"/>
    <header_item key="X-BBL-Client-Version" value="%s"/>
  </header>
</config>`, bambuClientVersion)
}

func buildBambuPlateJSON(model *loader.LoadedModel, nozzle *Nozzle) string {
	minX, maxX, minY, maxY, _ := meshExtents(model)
	payload := map[string]any{
		"bbox_all":        []float64{float64(minX), float64(minY), float64(maxX), float64(maxY)},
		"bbox_objects":    []any{},
		"bed_type":        "textured_plate",
		"filament_colors": []string{},
		"filament_ids":    []string{},
		"first_extruder":  1,
		"is_seq_print":    false,
		"nozzle_diameter": nozzle.diameterFloat(),
		"version":         2,
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// buildBambuProjectSettings merges machine + process + filament profiles and
// expands filament_* arrays to match num_filaments × len(variants). The result
// satisfies BambuStudio's PresetBundle validator (filament_colour nonempty;
// filament_extruder_variant / filament_self_index size match).
func buildBambuProjectSettings(
	printer *Printer,
	nozzle *Nozzle,
	machine map[string]any,
	filament map[string]any,
	paletteRGB [][3]uint8,
	layerHeight float32,
	variants []string,
) (string, error) {
	N := len(paletteRGB)
	variantCount := len(variants)
	data := make(map[string]any, len(machine)+len(filament)+32)
	for k, v := range machine {
		data[k] = v
	}
	delete(data, "name")

	// Merge process profile.
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

	// Merge filament profile — but expand each array to the needed size.
	fid, _ := filament["filament_id"].(string)
	if fid == "" {
		return "", fmt.Errorf("filament profile for %s nozzle %s is missing filament_id",
			printer.ID, nozzle.Diameter)
	}
	delete(filament, "name")
	for k, v := range filament {
		arr, ok := v.([]any)
		if !ok {
			// Scalar values (e.g. single strings) pass through as-is.
			data[k] = v
			continue
		}
		switch len(arr) {
		case 1:
			// Per-filament single value: replicate N times.
			data[k] = repeatAny(arr[0], N)
		case variantCount:
			// Per-filament × per-variant: interleave variantCount-sized block N times.
			out := make([]any, 0, N*variantCount)
			for i := 0; i < N; i++ {
				out = append(out, arr...)
			}
			data[k] = out
		default:
			// Unknown pattern — pass through. Bambu will size-check explicit
			// filament_* keys; anything else is noise for our purposes.
			data[k] = v
		}
	}

	// Stamp identity + session keys.
	data["name"] = "project_settings"
	data["from"] = "project"
	data["version"] = bambuClientVersion
	if processName != "" {
		data["print_settings_id"] = processName
	}
	data["printer_settings_id"] = nozzle.PrinterSettingsID
	data["printer_technology"] = "FFF"
	data["printer_model"] = printer.DisplayName

	// Per-filament overrides.
	hexColors := make([]any, N)
	filamentSettingsIDs := make([]any, N)
	filamentTypes := make([]any, N)
	filamentIDs := make([]any, N)
	filamentMultiColour := make([]any, N)
	filamentMap := make([]any, N)
	for i, p := range paletteRGB {
		hexColors[i] = fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		filamentSettingsIDs[i] = nozzle.FilamentSettingsID
		filamentTypes[i] = "PLA"
		filamentIDs[i] = fid
		filamentMultiColour[i] = hexColors[i]
		filamentMap[i] = "1"
	}
	data["filament_colour"] = hexColors
	data["filament_multi_colour"] = filamentMultiColour
	data["filament_settings_id"] = filamentSettingsIDs
	data["filament_type"] = filamentTypes
	data["filament_ids"] = filamentIDs
	data["filament_map"] = filamentMap
	data["filament_map_mode"] = "Auto For Flush"

	// filament_extruder_variant: N × variantCount; one cycle per filament.
	fev := make([]any, 0, N*variantCount)
	fsi := make([]any, 0, N*variantCount)
	for i := range N {
		for j := range variantCount {
			fev = append(fev, variants[j])
			fsi = append(fsi, strconv.Itoa(i+1))
		}
	}
	data["filament_extruder_variant"] = fev
	data["filament_self_index"] = fsi

	// Limit painted color depth so the slicer doesn't flood-fill large
	// regions with one filament.
	data["mmu_segmented_region_max_width"] = "1.5"

	// different_settings_to_system: length N+2 (print, N filaments, printer).
	// First entry flags the one print-setting we override; the rest stay empty.
	diff := make([]any, 2+N)
	diff[0] = "mmu_segmented_region_max_width"
	for i := 1; i < len(diff); i++ {
		diff[i] = ""
	}
	data["different_settings_to_system"] = diff

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// repeatAny returns a fresh slice containing v repeated n times.
func repeatAny(v any, n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// extractVariants reads machine.extruder_variant_list[0] as a comma-separated
// list of variant names ("Direct Drive Standard,Direct Drive High Flow").
func extractVariants(machine map[string]any) []string {
	raw, ok := machine["extruder_variant_list"]
	if !ok {
		return []string{"Direct Drive Standard"}
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return []string{"Direct Drive Standard"}
	}
	first, _ := list[0].(string)
	if first == "" {
		return []string{"Direct Drive Standard"}
	}
	parts := strings.Split(first, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"Direct Drive Standard"}
	}
	return out
}
