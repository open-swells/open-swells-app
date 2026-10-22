import datetime as dt
import gzip
import json
import os
import tempfile
import unittest
from unittest.mock import patch

import numpy as np

import gfs_to_contours
import spot_forecasts
from spot_forecasts import (
    FIELDS,
    load_spots,
    nearest_wet_cells,
    sample_spots,
    write_spot_forecasts,
)

# A small 1-degree lattice, north -> south, 0..360 longitudes like GFS.
LAT = np.arange(40.0, 29.0, -1.0)
LON = np.arange(230.0, 241.0, 1.0)
LON2D, LAT2D = np.meshgrid(LON, LAT)


def _data(height_value=2.0, land_east_of=236.0):
    """Wave dict shaped like composite_swell(); land east of a longitude."""
    land = LON2D >= land_east_of
    height = np.where(land, np.nan, height_value).astype(np.float32)
    partitions = []
    for n in (1, 2, 3):
        partitions.append(
            {
                "sequence": n,
                "height": np.full(LAT2D.shape, 0.5 * n, dtype=np.float32),
                "period": np.full(LAT2D.shape, 10.0 + n, dtype=np.float32),
                "direction": np.full(LAT2D.shape, 359.7, dtype=np.float32),
                "mask": land,
            }
        )
    return {
        "lon": LON2D.astype(np.float32),
        "lat": LAT2D.astype(np.float32),
        "height": height,
        "height_mask": land,
        "swell_partitions": partitions,
        "wind_sea": {
            "height": np.full(LAT2D.shape, 0.8, dtype=np.float32),
            "period": np.full(LAT2D.shape, 4.5, dtype=np.float32),
            "direction": np.full(LAT2D.shape, 300.2, dtype=np.float32),
        },
        "valid_date": dt.datetime(2026, 8, 10, 18),
    }


def _wind():
    return {
        "speed": np.full(LAT2D.shape, 5.0, dtype=np.float32),
        "direction": np.full(LAT2D.shape, 270.0, dtype=np.float32),
    }


class NearestWetCellTests(unittest.TestCase):
    def setUp(self):
        spot_forecasts._TREE_CACHE.clear()

    def test_land_spot_snaps_to_nearest_wet_cell(self):
        wet = LON2D < 236.0
        # A beach at 235.6E (-124.4) sits on a land-masked cell; the nearest
        # wet cell is one column west at 235E, same latitude.
        cells, km = nearest_wet_cells(
            LAT2D, LON2D, wet, np.array([35.0]), np.array([-124.4])
        )
        row, col = np.unravel_index(cells[0], LAT2D.shape)
        self.assertEqual((LAT[row], LON[col]), (35.0, 235.0))
        # 0.6 deg of longitude at 35N is ~55 km.
        self.assertAlmostEqual(float(km[0]), 54.7, delta=1.0)

    def test_spot_beyond_max_distance_is_unmatched(self):
        wet = LON2D < 231.0  # only the westernmost column is water
        cells, km = nearest_wet_cells(
            LAT2D, LON2D, wet, np.array([35.0]), np.array([-120.0]), max_km=100.0
        )
        self.assertEqual(cells[0], -1)
        self.assertTrue(np.isnan(km[0]))

    def test_longitude_distance_scales_with_latitude(self):
        # At 60N a degree of longitude is half a degree of latitude, so the
        # wet cell one column east must beat the one a row north.
        lat = np.array([[61.0, 61.0], [60.0, 60.0]])
        lon = np.array([[10.0, 11.0], [10.0, 11.0]])
        wet = np.array([[True, False], [False, True]])
        cells, _ = nearest_wet_cells(lat, lon, wet, np.array([60.0]), np.array([10.0]))
        self.assertEqual(np.unravel_index(cells[0], lat.shape), (1, 1))

    def test_tree_is_reused_for_same_mask(self):
        wet = LON2D < 236.0
        nearest_wet_cells(LAT2D, LON2D, wet, np.array([35.0]), np.array([-125.0]))
        nearest_wet_cells(LAT2D, LON2D, wet, np.array([36.0]), np.array([-125.0]))
        self.assertEqual(len(spot_forecasts._TREE_CACHE), 1)


class SampleSpotsTests(unittest.TestCase):
    def setUp(self):
        spot_forecasts._TREE_CACHE.clear()

    def test_samples_every_field_at_matched_cell(self):
        sample = sample_spots(
            _data(), _wind(), np.array([35.0, 35.0]), np.array([-125.0, -100.0])
        )
        first = dict(zip(FIELDS, sample["values"][0]))
        self.assertAlmostEqual(first["hs"], 2.0)
        self.assertAlmostEqual(first["h2"], 1.0)
        self.assertAlmostEqual(first["p3"], 13.0)
        self.assertAlmostEqual(first["ws"], 5.0)
        self.assertAlmostEqual(first["wd"], 270.0)
        self.assertAlmostEqual(first["hw"], 0.8)
        self.assertAlmostEqual(first["pw"], 4.5)
        self.assertAlmostEqual(float(sample["cell_lon"][0]), -125.0)
        # The inland spot has no cell within range: all NaN, index -1.
        self.assertEqual(sample["cells"][1], -1)
        self.assertTrue(np.isnan(sample["values"][1]).all())

    def test_wind_sea_omitted_when_hour_lacks_it(self):
        data = _data()
        data["wind_sea"] = None
        sample = sample_spots(data, _wind(), np.array([35.0]), np.array([-125.0]))
        values = dict(zip(FIELDS, sample["values"][0]))
        self.assertTrue(np.isnan(values["hw"]))
        self.assertAlmostEqual(values["h1"], 0.5)

    def test_wind_omitted_when_lattice_differs(self):
        wind = {
            "speed": np.zeros((2, 2), dtype=np.float32),
            "direction": np.zeros((2, 2), dtype=np.float32),
        }
        sample = sample_spots(_data(), wind, np.array([35.0]), np.array([-125.0]))
        values = dict(zip(FIELDS, sample["values"][0]))
        self.assertTrue(np.isnan(values["ws"]))
        self.assertAlmostEqual(values["hs"], 2.0)


class WriteSpotForecastsTests(unittest.TestCase):
    def setUp(self):
        spot_forecasts._TREE_CACHE.clear()

    def test_payload_layout_rounding_and_failed_hours(self):
        lats, lons = np.array([35.0, 35.0]), np.array([-125.0, -100.0])
        samples = {
            0: sample_spots(_data(2.004), _wind(), lats, lons),
            # position 1 (hour 3) failed: no sample
            2: sample_spots(_data(3.0), _wind(), lats, lons),
        }
        with tempfile.TemporaryDirectory() as tmp:
            meta = write_spot_forecasts(
                tmp, ["coast", "inland"], [0, 3, 6], samples, "20260810_18Z"
            )
            with gzip.open(os.path.join(tmp, "spot_forecasts.json.gz"), "rt") as f:
                payload = json.load(f)

        self.assertEqual(meta["spots"], 1)
        self.assertEqual(meta["spots_skipped"], 1)
        self.assertEqual(payload["forecast_start"], "20260810_18Z")
        self.assertEqual(payload["hours"], [0, 3, 6])
        self.assertEqual(payload["fields"], list(FIELDS))
        self.assertNotIn("inland", payload["spots"])
        coast = payload["spots"]["coast"]
        self.assertEqual(coast["cell"], [35.0, -125.0])
        self.assertEqual(coast["km"], 0.0)
        # A failed hour is null, never borrowed from a neighbor.
        self.assertEqual(coast["hs"], [2.0, None, 3.0])
        self.assertEqual(coast["p1"], [11.0, None, 11.0])
        # Directions are integers wrapped into 0..359.
        self.assertEqual(coast["d1"], [0, None, 0])
        self.assertEqual(coast["wd"], [270, None, 270])
        self.assertEqual(coast["hw"], [0.8, None, 0.8])
        self.assertEqual(coast["dw"], [300, None, 300])


class LoadSpotsTests(unittest.TestCase):
    def test_sample_point_override(self):
        spots = [
            {"id": "a", "lat": 33.0, "lon": -118.0},
            {"id": "b", "lat": 34.0, "lon": -119.0, "sample_lat": 33.9, "sample_lon": -119.2},
        ]
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(spots, f)
        try:
            ids, lats, lons = load_spots(f.name)
        finally:
            os.remove(f.name)
        self.assertEqual(ids, ["a", "b"])
        self.assertEqual(lats.tolist(), [33.0, 33.9])
        self.assertEqual(lons.tolist(), [-118.0, -119.2])


class ProcessHoursSpotSamplesTests(unittest.TestCase):
    def test_samples_collected_by_position(self):
        results = {
            0: ("000", True, None, {"hour": 0}),
            3: ("003", False, None, None),
            6: ("006", True, None, {"hour": 6}),
        }
        run_info = {}
        with patch.object(
            gfs_to_contours, "_process_single_hour", lambda h, **kw: results[h]
        ):
            gfs_to_contours.process_forecast_hours(
                [0, 3, 6], "20260810", "18", "unused_dir", workers=1, run_info=run_info
            )
        self.assertEqual(run_info["spot_samples"], {0: {"hour": 0}, 2: {"hour": 6}})


if __name__ == "__main__":
    unittest.main()
