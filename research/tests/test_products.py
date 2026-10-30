import ast
import json

import numpy as np
import pytest
import xarray as xr

from openswells_research import products


def _pipeline_constants(*names):
    """Evaluate module-level constants from gfs_to_contours.py without
    importing it (it needs pygrib)."""
    tree = ast.parse((products.PIPELINE_DIR / "gfs_to_contours.py").read_text())
    nodes: list[ast.stmt] = [n for n in tree.body if isinstance(n, ast.Assign)
             and any(isinstance(t, ast.Name) and t.id in names for t in n.targets)]
    scope = {}
    exec(compile(ast.Module(nodes, type_ignores=[]), "gfs_to_contours.py", "exec"), scope)  # noqa: S102 -- our own constants
    return [scope[n] for n in names]


def test_var_tables_match_pipeline():
    swell, wind = _pipeline_constants("SWELL_VARS", "WIND_VARS")
    assert products.SWELL_VARS == swell
    assert products.WIND_VARS == wind


def _forecast():
    lat = np.array([10.0, 8.33])
    lon = np.array([200.0, 201.67, 203.33])
    shape = (2, 2, 3)
    rng = np.random.default_rng(0)
    data = {}
    for k in (1, 2, 3):
        data[f"h{k}"] = rng.uniform(0.1, 3, shape)
        data[f"p{k}"] = rng.uniform(5, 18, shape)
        data[f"d{k}"] = rng.uniform(0, 359, shape)
    data["h3"][:, 0, 0] = np.nan  # partition 3 absent in one cell
    data["u10"] = np.full(shape, 5.0)  # westerly: blowing toward the east
    data["v10"] = np.zeros(shape)
    data["u10"][1, 1, 2] = np.nan  # land
    return xr.Dataset({k: (("hour", "latitude", "longitude"), v) for k, v in data.items()},
                      coords={"hour": [0, 6], "latitude": lat, "longitude": lon})


def test_write_run_round_trips_through_pipeline_reader(tmp_path):
    ds = _forecast()
    names = products.write_run(ds, tmp_path, "20261029_00Z")
    assert names == {"swell": "swell_20261029_00Z.bin", "wind": "wind_20261029_00Z.bin"}

    (tmp_path / "metadata.json").write_text(json.dumps(
        {"forecast_start": "20261029_00Z", "bundles": names}))
    back = products.read_run(tmp_path)
    assert list(back["hour"].values) == [0, 6]
    np.testing.assert_allclose(back["h1"], ds["h1"], atol=0.005)
    np.testing.assert_allclose(back["p2"], ds["p2"], atol=0.05)
    assert np.all(np.isnan(back["p3"][:, 0, 0]))  # whole partition dropped
    assert np.all(np.isfinite(back["p3"][:, 0, 1]))

    np.testing.assert_allclose(back["ws"].values[0], 5.0)
    np.testing.assert_allclose(back["wd"].values[0], 270.0)  # from the west
    assert np.isnan(back["ws"][1, 1, 2]) and np.isnan(back["v10"][1, 1, 2])


def test_read_run_rejects_legacy_metadata(tmp_path):
    (tmp_path / "metadata.json").write_text(json.dumps({"forecast_start": "20261020_18Z"}))
    with pytest.raises(ValueError, match="no bundles"):
        products.read_run(tmp_path)


@pytest.mark.parametrize("u,v,expected", [(0, -5, 0), (-5, 0, 90), (0, 5, 180), (5, 0, 270)])
def test_wind_direction_is_where_it_comes_from(u, v, expected):
    _, d = products.wind_speed_direction(np.float64(u), np.float64(v))
    assert d == pytest.approx(expected)
