"""Export a mesh with per-face material assignments as a 3MF file (OrcaSlicer/BambuStudio format)."""

import json
import uuid
import zipfile

import numpy as np

from .loader import LoadedModel

MAX_FILAMENTS = 16

# paint_color lookup table from OrcaSlicer/BambuStudio source (Model.cpp CONST_FILAMENTS).
# Index 0 = no filament, index N = filament N (1-based).
# Encoding is a nibble bitstream (LSB-first) where:
#   state 1 ("4") = filament 1, state 2 ("8") = filament 2,
#   state >= 3 uses 2 nibbles: first nibble = 0xC, second nibble = state - 3 → "0C","1C","2C",...
_PAINT_COLORS = [
    "",     "4",    "8",    "0C",   "1C",   "2C",   "3C",   "4C",
    "5C",   "6C",   "7C",   "8C",   "9C",   "AC",   "BC",   "CC",   "DC",
]


def _paint_color(palette_index: int) -> str:
    filament = palette_index + 1
    assert 1 <= filament <= MAX_FILAMENTS, f"palette_index {palette_index} out of range (max {MAX_FILAMENTS})"
    return _PAINT_COLORS[filament]


_CONTENT_TYPES = """\
<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
 <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
 <Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/>
</Types>"""

_RELS = """\
<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
 <Relationship Target="/3D/3dmodel.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>"""


def export_3mf(
    model: LoadedModel,
    assignments: np.ndarray,   # (F,) int — palette index per face
    output_path: str,
    palette_rgb: "np.ndarray | None" = None,  # (P, 3) uint8 — for filament colors in project settings
) -> None:
    outer_uuid   = str(uuid.uuid4())
    mesh_uuid    = str(uuid.uuid4())
    inst_uuid    = str(uuid.uuid4())
    build_uuid   = str(uuid.uuid4())

    object_rels = f"""\
<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
 <Relationship Target="/3D/Objects/object_1.model" Id="rel-1" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>"""

    main_model = f"""\
<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" \
xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06" \
unit="millimeter" xml:lang="en-US" requiredextensions="p">
 <resources>
  <object id="2" p:UUID="{outer_uuid}" type="model">
   <components>
    <component p:path="/3D/Objects/object_1.model" objectid="1" p:UUID="{mesh_uuid}" transform="1 0 0 0 1 0 0 0 1 0 0 0"/>
   </components>
  </object>
 </resources>
 <build p:UUID="{build_uuid}">
  <item objectid="2" p:UUID="{inst_uuid}" transform="1 0 0 0 1 0 0 0 1 0 0 0" printable="1"/>
 </build>
</model>"""

    object_model = _build_object_model(model, assignments)
    model_settings = _build_model_settings(len(model.mesh.faces))

    with zipfile.ZipFile(output_path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=5) as z:
        z.writestr("[Content_Types].xml", _CONTENT_TYPES)
        z.writestr("_rels/.rels", _RELS)
        z.writestr("3D/3dmodel.model", main_model)
        z.writestr("3D/_rels/3dmodel.model.rels", object_rels)
        z.writestr("3D/Objects/object_1.model", object_model)
        z.writestr("Metadata/model_settings.config", model_settings)
        if palette_rgb is not None:
            z.writestr("Metadata/project_settings.config",
                       _build_project_settings(palette_rgb))


def _build_object_model(model: LoadedModel, assignments: np.ndarray) -> str:
    obj_uuid = str(uuid.uuid4())
    vertices = model.mesh.vertices
    faces = model.mesh.faces

    lines: list[str] = []
    lines.append('<?xml version="1.0" encoding="UTF-8"?>')
    lines.append(
        '<model unit="millimeter" xml:lang="en-US"'
        ' xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"'
        ' xmlns:BambuStudio="http://schemas.bambulab.com/package/2021"'
        ' xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"'
        ' requiredextensions="p">'
    )
    lines.append(' <metadata name="BambuStudio:3mfVersion">1</metadata>')
    lines.append(" <resources>")
    lines.append(f'  <object id="1" p:UUID="{obj_uuid}" type="model">')
    lines.append("   <mesh>")

    lines.append("    <vertices>")
    for x, y, z in vertices:
        lines.append(f'     <vertex x="{x:.6f}" y="{y:.6f}" z="{z:.6f}"/>')
    lines.append("    </vertices>")

    lines.append("    <triangles>")
    for (v1, v2, v3), mat in zip(faces, assignments):
        pc = _paint_color(int(mat))
        lines.append(f'     <triangle v1="{v1}" v2="{v2}" v3="{v3}" paint_color="{pc}"/>')
    lines.append("    </triangles>")

    lines.append("   </mesh>")
    lines.append("  </object>")
    lines.append(" </resources>")
    lines.append(" <build/>")
    lines.append("</model>")

    return "\n".join(lines)


def _build_project_settings(palette_rgb: np.ndarray) -> str:
    """Minimal project_settings.config with filament colors so slicers load them on open."""
    hex_colors = [f"#{r:02X}{g:02X}{b:02X}" for r, g, b in palette_rgb]
    return json.dumps({
        "filament_colour": hex_colors,
        "filament_type": ["PLA"] * len(hex_colors),
    }, indent=2)


def _build_model_settings(face_count: int) -> str:
    return f"""\
<?xml version="1.0" encoding="UTF-8"?>
<config>
  <object id="2">
    <metadata key="name" value="text2filament_output"/>
    <metadata key="extruder" value="1"/>
    <metadata face_count="{face_count}"/>
    <part id="1" subtype="normal_part">
      <metadata key="name" value="text2filament_output"/>
      <metadata key="extruder" value="1"/>
    </part>
  </object>
</config>"""
