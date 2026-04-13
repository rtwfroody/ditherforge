package loader

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
)

// 3MF XML structures (core spec subset).

type model3mf struct {
	XMLName   xml.Name       `xml:"model"`
	Resources resources3mf   `xml:"resources"`
}

type resources3mf struct {
	BaseMaterials []baseMaterialGroup3mf `xml:"basematerials"`
	Objects       []object3mf            `xml:"object"`
}

type baseMaterialGroup3mf struct {
	ID    int              `xml:"id,attr"`
	Bases []baseMaterial3mf `xml:"base"`
}

type baseMaterial3mf struct {
	Name         string `xml:"name,attr"`
	DisplayColor string `xml:"displaycolor,attr"`
}

type object3mf struct {
	ID   int     `xml:"id,attr"`
	Name string  `xml:"name,attr"`
	Mesh mesh3mf `xml:"mesh"`
}

type mesh3mf struct {
	Vertices verticesWrap3mf `xml:"vertices"`
	Triangles trianglesWrap3mf `xml:"triangles"`
}

type verticesWrap3mf struct {
	Vertex []vertex3mf `xml:"vertex"`
}

type trianglesWrap3mf struct {
	Triangle []triangle3mf `xml:"triangle"`
}

type vertex3mf struct {
	X float32 `xml:"x,attr"`
	Y float32 `xml:"y,attr"`
	Z float32 `xml:"z,attr"`
}

type triangle3mf struct {
	V1         uint32  `xml:"v1,attr"`
	V2         uint32  `xml:"v2,attr"`
	V3         uint32  `xml:"v3,attr"`
	PID        *int    `xml:"pid,attr"`
	P1         *int    `xml:"p1,attr"`
	PaintColor string  `xml:"paint_color,attr"`
}

// paintColorToFilament maps OrcaSlicer paint_color hex strings to 1-based
// filament indices. Mirrors the table in export3mf.
var paintColorToFilament = map[string]int{
	"4": 1, "8": 2, "0C": 3, "1C": 4, "2C": 5, "3C": 6, "4C": 7,
	"5C": 8, "6C": 9, "7C": 10, "8C": 11, "9C": 12, "AC": 13, "BC": 14, "CC": 15, "DC": 16,
}

// parseHexColor parses a hex color string like "#FF0000" or "#FF0000FF" into RGBA.
func parseHexColor(s string) ([4]uint8, error) {
	s = strings.TrimPrefix(s, "#")
	var r, g, b, a uint8
	a = 255
	switch len(s) {
	case 6:
		v, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return [4]uint8{}, fmt.Errorf("invalid hex color %q: %w", s, err)
		}
		r = uint8(v >> 16)
		g = uint8(v >> 8)
		b = uint8(v)
	case 8:
		v, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return [4]uint8{}, fmt.Errorf("invalid hex color %q: %w", s, err)
		}
		r = uint8(v >> 24)
		g = uint8(v >> 16)
		b = uint8(v >> 8)
		a = uint8(v)
	default:
		return [4]uint8{}, fmt.Errorf("invalid hex color %q: expected 6 or 8 hex digits", s)
	}
	return [4]uint8{r, g, b, a}, nil
}

// readDefaultExtruder reads the default extruder (1-based) from model_settings.config.
// Returns 1 if not found or on error.
func readDefaultExtruder(f *zip.File) (int, error) {
	rc, err := f.Open()
	if err != nil {
		return 1, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return 1, err
	}
	// Simple XML parse: look for <metadata key="extruder" value="N"/> inside <object>.
	type metadata struct {
		Key   string `xml:"key,attr"`
		Value string `xml:"value,attr"`
	}
	type object struct {
		Metadata []metadata `xml:"metadata"`
	}
	type config struct {
		Objects []object `xml:"object"`
	}
	var cfg config
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return 1, err
	}
	for _, obj := range cfg.Objects {
		for _, md := range obj.Metadata {
			if md.Key == "extruder" {
				if v, err := strconv.Atoi(md.Value); err == nil && v >= 1 {
					return v, nil
				}
			}
		}
	}
	return 1, nil
}

// readFilamentColors reads filament_colour and mixed_filament_definitions from
// project_settings.config. Returns the complete color table: physical colors
// followed by blended mixed filament colors (FullSpectrum OrcaSlicer extension).
func readFilamentColors(f *zip.File) ([][4]uint8, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("opening: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}
	fc, ok := settings["filament_colour"]
	if !ok {
		return nil, fmt.Errorf("missing filament_colour key")
	}
	arr, ok := fc.([]interface{})
	if !ok {
		return nil, fmt.Errorf("filament_colour is not an array")
	}
	var colors [][4]uint8
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			continue
		}
		c, err := parseHexColor(s)
		if err != nil {
			continue
		}
		colors = append(colors, c)
	}

	// Parse mixed_filament_definitions (FullSpectrum extension).
	// Each enabled, non-deleted row produces a blended color appended after
	// the physical colors.
	if mfd, ok := settings["mixed_filament_definitions"]; ok {
		if mfdStr, ok := mfd.(string); ok && mfdStr != "" {
			mixed := parseMixedFilaments(mfdStr, colors)
			colors = append(colors, mixed...)
		}
	}

	return colors, nil
}

// parseMixedFilaments parses the mixed_filament_definitions string and returns
// blended colors for each enabled, non-deleted mixed filament.
func parseMixedFilaments(defs string, physicalColors [][4]uint8) [][4]uint8 {
	var result [][4]uint8
	for _, row := range strings.Split(defs, ";") {
		if row == "" {
			continue
		}
		fields := strings.Split(row, ",")
		if len(fields) < 5 {
			continue
		}
		compA, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		compB, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		enabled, err := strconv.Atoi(fields[2])
		if err != nil || enabled == 0 {
			continue
		}
		mixBPct, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}

		// Check deleted flag (field starting with 'd').
		deleted := false
		for _, f := range fields[5:] {
			if strings.HasPrefix(f, "d") {
				deleted = f[1:] == "1"
				break
			}
		}
		if deleted {
			continue
		}

		// Blend the two component colors.
		if compA < 1 || compA > len(physicalColors) || compB < 1 || compB > len(physicalColors) {
			continue
		}
		ca := physicalColors[compA-1]
		cb := physicalColors[compB-1]
		wA := 100 - mixBPct
		wB := mixBPct
		total := wA + wB
		if total == 0 {
			total = 1
		}
		blended := [4]uint8{
			uint8((int(ca[0])*wA + int(cb[0])*wB) / total),
			uint8((int(ca[1])*wA + int(cb[1])*wB) / total),
			uint8((int(ca[2])*wA + int(cb[2])*wB) / total),
			255,
		}
		result = append(result, blended)
	}
	return result
}

// Load3MF loads a 3MF file and returns a LoadedModel.
// Supports OrcaSlicer/BambuStudio paint_color attributes and standard 3MF
// basematerials. The scale parameter is applied directly (3MF is already in mm).
//
// Limitation: ignores <build> item transforms and <components> composition.
// Multi-part assemblies will be merged at their local coordinates.
// parse3MFModels opens a 3MF file and parses all .model XML files within it.
// Returns the zip reader (caller must close), the parsed models, and any
// auxiliary config files found.
type parsed3MFResult struct {
	zr                  *zip.ReadCloser
	models              []model3mf
	projectSettingsFile *zip.File
	modelSettingsFile   *zip.File
}

func parse3MFModels(path string) (*parsed3MFResult, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening 3MF: %w", err)
	}

	var modelFiles []*zip.File
	var projectSettingsFile *zip.File
	var modelSettingsFile *zip.File
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".model") {
			modelFiles = append(modelFiles, f)
		}
		if strings.EqualFold(f.Name, "Metadata/project_settings.config") {
			projectSettingsFile = f
		}
		if strings.EqualFold(f.Name, "Metadata/model_settings.config") {
			modelSettingsFile = f
		}
	}
	if len(modelFiles) == 0 {
		zr.Close()
		return nil, fmt.Errorf("3MF contains no .model files")
	}

	var allModels []model3mf
	for _, mf := range modelFiles {
		rc, err := mf.Open()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("opening %s: %w", mf.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			zr.Close()
			return nil, fmt.Errorf("reading %s: %w", mf.Name, err)
		}
		var m model3mf
		if err := xml.Unmarshal(data, &m); err != nil {
			zr.Close()
			return nil, fmt.Errorf("parsing %s: %w", mf.Name, err)
		}
		allModels = append(allModels, m)
	}

	return &parsed3MFResult{
		zr:                  zr,
		models:              allModels,
		projectSettingsFile: projectSettingsFile,
		modelSettingsFile:   modelSettingsFile,
	}, nil
}

func Load3MF(path string, scale float32, objectIndex int) (*LoadedModel, error) {
	parsed, err := parse3MFModels(path)
	if err != nil {
		return nil, err
	}
	defer parsed.zr.Close()

	allModels := parsed.models
	projectSettingsFile := parsed.projectSettingsFile
	modelSettingsFile := parsed.modelSettingsFile

	// Collect basematerials across all models.
	type bmGroup struct {
		id    int
		bases []baseMaterial3mf
	}
	var bmGroups []bmGroup
	for _, m := range allModels {
		for _, bg := range m.Resources.BaseMaterials {
			bmGroups = append(bmGroups, bmGroup{id: bg.ID, bases: bg.Bases})
		}
	}

	// Detect color mode: check if any triangle uses paint_color.
	hasPaintColor := false
	for _, m := range allModels {
		for _, obj := range m.Resources.Objects {
			for _, tri := range obj.Mesh.Triangles.Triangle {
				if tri.PaintColor != "" {
					hasPaintColor = true
					break
				}
			}
			if hasPaintColor {
				break
			}
		}
		if hasPaintColor {
			break
		}
	}

	// Read filament colors from project_settings.config if using paint_color mode.
	var filamentColors [][4]uint8
	if hasPaintColor {
		if projectSettingsFile == nil {
			fmt.Fprintf(os.Stderr, "Warning: 3MF has paint_color attributes but no project_settings.config; colors will be white\n")
		} else {
			fc, err := readFilamentColors(projectSettingsFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: reading project_settings.config: %v; paint colors may be wrong\n", err)
			}
			filamentColors = fc
		}
	}

	// Read default extruder from model_settings.config (1-based, defaults to 1).
	defaultExtruder := 1
	if modelSettingsFile != nil {
		if ext, err := readDefaultExtruder(modelSettingsFile); err == nil {
			defaultExtruder = ext
		}
	}

	// Build merged mesh from all objects across all models.
	var allVerts [][3]float32
	var allFaces [][3]uint32
	var allFaceBaseColor [][4]uint8

	nonEmptyIdx := 0
	for _, m := range allModels {
		for _, obj := range m.Resources.Objects {
			mesh := obj.Mesh
			if len(mesh.Vertices.Vertex) == 0 || len(mesh.Triangles.Triangle) == 0 {
				continue
			}

			if objectIndex >= 0 {
				if nonEmptyIdx != objectIndex {
					nonEmptyIdx++
					continue
				}
				nonEmptyIdx++
			}

			vertOffset := uint32(len(allVerts))
			nv := uint32(len(mesh.Vertices.Vertex))

			// Add vertices with scale.
			for _, v := range mesh.Vertices.Vertex {
				allVerts = append(allVerts, [3]float32{
					v.X * scale,
					v.Y * scale,
					v.Z * scale,
				})
			}

			// Add faces with color resolution.
			for _, tri := range mesh.Triangles.Triangle {
				if tri.V1 >= nv || tri.V2 >= nv || tri.V3 >= nv {
					return nil, fmt.Errorf("triangle references vertex out of range in object %d", obj.ID)
				}
				allFaces = append(allFaces, [3]uint32{
					tri.V1 + vertOffset,
					tri.V2 + vertOffset,
					tri.V3 + vertOffset,
				})

				// Default color: in paint_color mode, unpainted faces use
				// the default extruder from model_settings.config.
				faceColor := [4]uint8{255, 255, 255, 255}
				if hasPaintColor && defaultExtruder-1 < len(filamentColors) {
					faceColor = filamentColors[defaultExtruder-1]
				}

				if hasPaintColor {
					// OrcaSlicer mode: paint_color → filament index → color.
					if tri.PaintColor != "" {
						if filIdx, ok := paintColorToFilament[tri.PaintColor]; ok {
							if filIdx-1 < len(filamentColors) {
								faceColor = filamentColors[filIdx-1]
							}
						}
					}
				} else if len(bmGroups) > 0 {
					// Standard 3MF mode: pid/p1 → basematerial color.
					if tri.PID != nil && tri.P1 != nil {
						for _, bg := range bmGroups {
							if bg.id == *tri.PID {
								idx := *tri.P1
								if idx >= 0 && idx < len(bg.bases) {
									if c, err := parseHexColor(bg.bases[idx].DisplayColor); err == nil {
										faceColor = c
									}
								}
								break
							}
						}
					}
				}

				allFaceBaseColor = append(allFaceBaseColor, faceColor)
			}
		}
	}

	if len(allFaces) == 0 {
		return nil, fmt.Errorf("3MF contains no mesh data")
	}

	// Build LoadedModel. 3MF meshes have no textures or UVs.
	nFaces := len(allFaces)
	nVerts := len(allVerts)

	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})

	uvs := make([][2]float32, nVerts)
	faceTexIdx := make([]int32, nFaces)
	noTextureMask := make([]bool, nFaces)
	faceMeshIdx := make([]int32, nFaces)
	for i := range faceTexIdx {
		faceTexIdx[i] = 1 // sentinel: len(Textures) = 1
		noTextureMask[i] = true
	}

	return &LoadedModel{
		Vertices:       allVerts,
		Faces:          allFaces,
		UVs:            uvs,
		Textures:       []image.Image{placeholder},
		FaceTextureIdx: faceTexIdx,
		FaceBaseColor:  allFaceBaseColor,
		NoTextureMask:  noTextureMask,
		FaceMeshIdx:    faceMeshIdx,
		NumMeshes:      1,
	}, nil
}
