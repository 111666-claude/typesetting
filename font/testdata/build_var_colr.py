"""Build VAR-COLRv1.ttf, a minimal variable COLRv1 fixture.

Run from this directory:

    python3 -P build_var_colr.py

The source font is the toys/GVAR-no-HVAR.ttf fixture from the
github.com/go-text/typesetting-utils module. Its absolute path in the Go
module cache can be overridden with the SRC_FONT environment variable.
The output is always written next to this script.

-P keeps the interpreter from loading any module shipped next to the script.
Requires fontTools (https://github.com/fonttools/fonttools).
"""

import os

from fontTools.ttLib import TTFont, newTable
from fontTools.ttLib.tables import otTables
from fontTools.ttLib.tables.C_P_A_L_ import Color
from fontTools.varLib.varStore import buildVarData, buildVarStore
from fontTools.colorLib import builder as color_builder

HERE = os.path.dirname(os.path.abspath(__file__))
DST = os.path.join(HERE, "VAR-COLRv1.ttf")
SRC = os.environ.get(
    "SRC_FONT",
    os.path.join(
        os.path.expanduser("~"),
        "go/pkg/mod/github.com/go-text/",
        "typesetting-utils@v0.0.0-20261009215314-bb530f4842f3",
        "opentype/toys/GVAR-no-HVAR.ttf",
    ),
)

font = TTFont(SRC)
axis_count = len(font["fvar"].axes)  # wght, wdth -> 2

# CPAL with one palette of two colors.
cpal = newTable("CPAL")
cpal.version = 0
cpal.numPaletteEntries = 2
cpal.palettes = [[Color(255, 0, 0, 255), Color(0, 0, 255, 255)]]
cpal.paletteTypes = []
cpal.paletteLabels = []
cpal.paletteEntryLabels = []
font["CPAL"] = cpal

# One region spanning the whole wght axis (axis index 0): start -1, peak 1,
# end 1. With start == peak == 1 the region is inactive at the default
# coordinate 0, so only positive wght locations get the delta.
region_axes = []
for axis_index in range(axis_count):
    axis = otTables.VarRegionAxis()
    if axis_index == 0:
        axis.StartCoord, axis.PeakCoord, axis.EndCoord = -1.0, 1.0, 1.0
    else:
        axis.StartCoord, axis.PeakCoord, axis.EndCoord = 0.0, 0.0, 0.0
    region_axes.append(axis)
region = otTables.VarRegion()
region.VarRegionAxis = region_axes
region.VarRegionAxisCount = len(region_axes)

region_list = otTables.VarRegionList()
region_list.Region = [region]
region_list.RegionCount = 1
region_list.AxisCount = axis_count

# One VarData: item 0 has a +42 delta in region 0.
var_data = buildVarData([0], [[42]])
var_store = buildVarStore(region_list, [var_data])

colr_table = color_builder.buildCOLR(
    {".notdef": {"Format": 2, "PaletteIndex": 0, "Alpha": 1.0}},
    version=1,
    glyphMap={name: gid for gid, name in enumerate(font.getGlyphOrder())},
    varStore=var_store,
)
font["COLR"] = colr_table

font.save(DST)
print("saved", DST)
