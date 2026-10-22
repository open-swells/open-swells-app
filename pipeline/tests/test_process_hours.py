import unittest
from concurrent.futures import ThreadPoolExecutor
from threading import Event
from unittest.mock import patch

import gfs_to_contours


class ProcessForecastHoursTests(unittest.TestCase):
    def test_tally_and_first_bounds_published(self):
        results = {
            0: ("000", True, {"north": 85.0}, None),
            3: ("003", False, None, None),
            6: ("006", True, {"north": 42.0}, None),
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
                return ("000", True, {"north": 85.0}, None)
            later_hour_finished.set()
            return ("003", True, {"north": 42.0}, None)

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
            return ("000", True, None, None)

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
