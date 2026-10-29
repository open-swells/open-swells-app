import os
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from threading import Event
from unittest.mock import patch

import numpy as np

import bundle
import gfs_to_contours


class ProcessForecastHoursTests(unittest.TestCase):
    def test_tally_and_first_bounds_published(self):
        results = {
            0: ("000", True, {"north": 85.0}, None, None),
            3: ("003", False, None, None, None),
            6: ("006", True, {"north": 42.0}, None, None),
        }

        def fake_hour(forecast_hour, **kwargs):
            return results[forecast_hour]

        run_info = {}
        with patch.object(gfs_to_contours, "_process_single_hour", fake_hour):
            successes, failures = gfs_to_contours.process_forecast_hours(
                [0, 3, 6], "20260712", "12", "unused_dir", workers=1, run_info=run_info
            )

        self.assertEqual((successes, failures), (2, 1))
        # First successful hour's bounds win; later ones must not overwrite.
        self.assertEqual(run_info["heatmap_bounds"], {"north": 85.0})
        # Only hours with frames are published; the failed hour is left out.
        self.assertEqual(run_info["hours"], [0, 6])

    def test_parallel_bounds_follow_forecast_order_not_completion_order(self):
        later_hour_finished = Event()

        def fake_hour(forecast_hour, **kwargs):
            if forecast_hour == 0:
                self.assertTrue(later_hour_finished.wait(timeout=1))
                return ("000", True, {"north": 85.0}, None, None)
            later_hour_finished.set()
            return ("003", True, {"north": 42.0}, None, None)

        run_info = {}
        with (
            patch.object(gfs_to_contours, "_process_single_hour", fake_hour),
            patch.object(gfs_to_contours, "ProcessPoolExecutor", ThreadPoolExecutor),
        ):
            successes, failures = gfs_to_contours.process_forecast_hours(
                [0, 3], "20260712", "12", "unused_dir", workers=2, run_info=run_info
            )

        self.assertEqual((successes, failures), (2, 0))
        self.assertEqual(run_info["heatmap_bounds"], {"north": 85.0})
        # Ascending forecast order even though hour 3 finished first.
        self.assertEqual(run_info["hours"], [0, 3])

    def test_worker_kwargs_forwarded(self):
        seen = {}

        def fake_hour(forecast_hour, **kwargs):
            seen.update(kwargs)
            return ("000", True, None, None, None)

        with patch.object(gfs_to_contours, "_process_single_hour", fake_hour):
            gfs_to_contours.process_forecast_hours(
                [0],
                "20260712",
                "12",
                "unused_dir",
                stride=2,
                smoothing_sigma=0.5,
                simplify_tolerance=None,
                arrow_stride=5,
                workers=1,
            )

        self.assertEqual(seen["stride"], 2)
        self.assertEqual(seen["smoothing_sigma"], 0.5)
        self.assertIsNone(seen["simplify_tolerance"])
        self.assertEqual(seen["arrow_stride"], 5)
        self.assertEqual(seen["date_str"], "20260712")
        self.assertEqual(seen["run_hour"], "12")

    def test_layers_assembled_into_bundles_in_hour_order(self):
        lat = np.array([10.0, 0.0])
        lon = np.array([0.0, 1.0, 2.0])

        def layers(value):
            grid = np.full((2, 3), value)
            return {
                "swell": {v["name"]: bundle.pack_grid(grid, v["scale"]) for v in gfs_to_contours.SWELL_VARS},
                "wind": {v["name"]: bundle.pack_grid(grid, v["scale"]) for v in gfs_to_contours.WIND_VARS},
                "contours": {"geojson": bundle.pack_document('{"features":[%d]}' % value)},
                "axes": (lat, lon),
            }

        results = {
            0: ("000", True, None, None, layers(1)),
            3: ("003", False, None, None, None),
            6: ("006", True, None, None, layers(2)),
        }

        def fake_hour(forecast_hour, **kwargs):
            return results[forecast_hour]

        run_info = {}
        with tempfile.TemporaryDirectory() as tmp:
            with patch.object(gfs_to_contours, "_process_single_hour", fake_hour):
                gfs_to_contours.process_forecast_hours(
                    [6, 3, 0], "20260712", "12", tmp, workers=1, run_info=run_info
                )
            self.assertEqual(run_info["bundles"], {
                "swell": "swell_20260712_12Z.bin",
                "wind": "wind_20260712_12Z.bin",
                "contours": "contours_20260712_12Z.bin",
            })
            swell_header, swell = bundle.read_bundle(os.path.join(tmp, run_info["bundles"]["swell"]))
            wind_header, wind = bundle.read_bundle(os.path.join(tmp, run_info["bundles"]["wind"]))
            contour_header, contours = bundle.read_bundle(os.path.join(tmp, run_info["bundles"]["contours"]))

        self.assertEqual(swell_header["hours"], [0, 6])
        self.assertEqual(swell_header["run"], "20260712_12Z")
        self.assertEqual(swell_header["lat"], [10.0, 0.0])
        self.assertEqual(len(swell_header["chunks"]), 2 * 9)
        self.assertTrue(np.all(swell[6]["h3"] == 2.0))
        self.assertTrue(np.all(wind[0]["s"] == 1.0))
        self.assertEqual(contour_header["hours"], [0, 6])
        self.assertEqual(contours[6]["geojson"], {"features": [2]})

    def test_default_workers_env_override(self):
        with patch.dict("os.environ", {"PARALLEL_HOURS": "6"}):
            self.assertEqual(gfs_to_contours.default_workers(), 6)
        with patch.dict("os.environ", {"PARALLEL_HOURS": "0"}):
            self.assertEqual(gfs_to_contours.default_workers(), 1)
        with patch.dict("os.environ", {}, clear=False):
            import os

            os.environ.pop("PARALLEL_HOURS", None)
            self.assertGreaterEqual(gfs_to_contours.default_workers(), 1)
            self.assertLessEqual(gfs_to_contours.default_workers(), 4)


if __name__ == "__main__":
    unittest.main()
