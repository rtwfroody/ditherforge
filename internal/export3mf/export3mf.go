// Package export3mf writes a 3MF file in BambuStudio/OrcaSlicer format.
package export3mf

import (
	"archive/zip"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rtwfroody/text2filament/internal/loader"
)

//go:embed snapmaker_u1_04.json
var snapmakerU1Profile []byte

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

const contentTypes = `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
 <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
 <Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/>
</Types>`

const rels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
 <Relationship Target="/3D/3dmodel.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>`

// Export writes the model and face assignments to a 3MF file.
func Export(model *loader.LoadedModel, assignments []int32, outputPath string, paletteRGB [][3]uint8) error {
	outerUUID := newUUID()
	meshUUID := newUUID()
	instUUID := newUUID()
	buildUUID := newUUID()

	// Compute XY translation to center model on build plate.
	// Build plate center for Snapmaker U1: (135.5, 136).
	const plateX, plateY = 135.5, 136.0
	var minX, maxX, minY, maxY float32
	if len(model.Vertices) > 0 {
		minX, maxX = model.Vertices[0][0], model.Vertices[0][0]
		minY, maxY = model.Vertices[0][1], model.Vertices[0][1]
		for _, v := range model.Vertices[1:] {
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
		}
	}
	tx := plateX - float64(minX+maxX)/2
	ty := plateY - float64(minY+maxY)/2

	objectRels := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
 <Relationship Target="/3D/Objects/object_1.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>`

	transform := fmt.Sprintf("1 0 0 0 1 0 0 0 1 %.4f %.4f 0", tx, ty)
	mainModel := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" unit="millimeter" xml:lang="en-US" requiredextensions="p">
 <resources>
  <object id="2" p:UUID="%s" type="model">
   <components>
    <component p:path="/3D/Objects/object_1.model" objectid="1" p:UUID="%s" transform="%s"/>
   </components>
  </object>
 </resources>
 <build p:UUID="%s">
  <item objectid="2" p:UUID="%s" transform="1 0 0 0 1 0 0 0 1 0 0 0" printable="1"/>
 </build>
</model>`, outerUUID, meshUUID, transform, buildUUID, instUUID)

	objectModel, err := buildObjectModel(model, assignments)
	if err != nil {
		return err
	}
	modelSettings := buildModelSettings(len(model.Faces))

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
	if err := writeEntry("3D/Objects/object_1.model", objectModel); err != nil {
		return err
	}
	if err := writeEntry("Metadata/model_settings.config", modelSettings); err != nil {
		return err
	}
	if paletteRGB != nil {
		ps, err := buildProjectSettings(paletteRGB)
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

	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<model unit="millimeter" xml:lang="en-US"`)
	sb.WriteString(` xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"`)
	sb.WriteString(` xmlns:BambuStudio="http://schemas.bambulab.com/package/2021"`)
	sb.WriteString(` xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"`)
	sb.WriteString(` requiredextensions="p">` + "\n")
	sb.WriteString(` <metadata name="BambuStudio:3mfVersion">1</metadata>` + "\n")
	sb.WriteString(` <resources>` + "\n")
	fmt.Fprintf(&sb, `  <object id="1" p:UUID="%s" type="model">`+"\n", objUUID)
	sb.WriteString(`   <mesh>` + "\n")

	sb.WriteString(`    <vertices>` + "\n")
	for _, v := range model.Vertices {
		fmt.Fprintf(&sb, `     <vertex x="%.6f" y="%.6f" z="%.6f"/>`+"\n", v[0], v[1], v[2])
	}
	sb.WriteString(`    </vertices>` + "\n")

	sb.WriteString(`    <triangles>` + "\n")
	for fi, face := range model.Faces {
		pc := paintColor(int(assignments[fi]))
		fmt.Fprintf(&sb, `     <triangle v1="%d" v2="%d" v3="%d" paint_color="%s"/>`+"\n",
			face[0], face[1], face[2], pc)
	}
	sb.WriteString(`    </triangles>` + "\n")

	sb.WriteString(`   </mesh>` + "\n")
	sb.WriteString(`  </object>` + "\n")
	sb.WriteString(` </resources>` + "\n")
	sb.WriteString(` <build/>` + "\n")
	sb.WriteString(`</model>`)

	return sb.String(), nil
}

func buildProjectSettings(paletteRGB [][3]uint8) (string, error) {
	// Start with the embedded Snapmaker U1 (0.4mm nozzle) profile.
	data := map[string]interface{}{}
	if err := json.Unmarshal(snapmakerU1Profile, &data); err != nil {
		return "", fmt.Errorf("parsing embedded profile: %w", err)
	}

	data["name"] = "project_settings"
	data["printer_settings_id"] = "Snapmaker U1 (0.4 nozzle)"
	data["printer_technology"] = "FFF"

	// Set filament info.
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

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildModelSettings(faceCount int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<config>
  <object id="2">
    <metadata key="name" value="text2filament_output"/>
    <metadata key="extruder" value="1"/>
    <metadata face_count="%d"/>
    <part id="1" subtype="normal_part">
      <metadata key="name" value="text2filament_output"/>
      <metadata key="extruder" value="1"/>
    </part>
  </object>
</config>`, faceCount)
}
