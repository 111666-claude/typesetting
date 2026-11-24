# Reproducing VAR-COLRv1.ttf

`VAR-COLRv1.ttf` is a test-only font generated from the
`toys/GVAR-no-HVAR.ttf` fixture of the
[`github.com/go-text/typesetting-utils`](https://github.com/go-text/typesetting-utils)
module. The source font exposes two variation axes (`wght`, `wdth`) and an
`avar` segment map; the generated file adds a `CPAL` table and a COLR version
1 table whose `ItemVariationStore` contains one variation region covering the
positive side of the `wght` axis (start -1, peak +1, end +1) with a single
delta set of +42.

The variation plan tests resolve this store against the normalized
coordinates produced by a `VariationPlan`:

- default location (`wght=400`) -> delta 0,
- `wght=900` (normalized +1.0) -> delta 42,
- `wght=100` (normalized -1.0) -> delta 0, outside of the region peak.

Regenerate it with [fontTools](https://github.com/fonttools/fonttools):

```sh
python3 -P build_var_colr.py
```

`-P` keeps the interpreter from loading any module shipped next to the script.
