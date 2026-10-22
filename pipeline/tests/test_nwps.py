import datetime as dt
import json
import os
import tempfile
import unittest
from unittest.mock import patch

import numpy as np

import gfs_to_contours
import nwps


class DomainConfigTests(unittest.TestCase):
    def test_default_domains(self):
        with patch.dict("os.environ", {}, clear=False):
            import os

            os.environ.pop("NWPS_DOMAINS", None)
            self.assertEqual(nwps.domains_from_env(), [("wr", "lox"), ("wr", "sgx")])

    def test_custom_domains_and_disable(self):
        with patch.dict("os.environ", {"NWPS_DOMAINS": "ER/OKX, wr/mtr"}):
            self.assertEqual(nwps.domains_from_env(), [("er", "okx"), ("wr", "mtr")])
        with patch.dict("os.environ", {"NWPS_DOMAINS": ""}):
            self.assertEqual(nwps.domains_from_env(), [])

    def test_malformed_domain_raises(self):
        with patch.dict("os.environ", {"NWPS_DOMAINS": "lox"}):
            with self.assertRaises(ValueError):
                nwps.domains_from_env()

    def test_grids(self):
        with patch.dict("os.environ", {"NWPS_GRIDS": "cg1, cg2"}):
            self.assertEqual(nwps.grids_from_env(), ["CG1", "CG2"])


class SelectFramesTests(unittest.TestCase):
    START = dt.datetime(2026, 7, 16, 12)
    STEPS = set(range(145))  # hourly f000-f144

    def test_same_cycle_maps_one_to_one(self):
        frames = nwps.select_frames(
            [0, 1, 2, 120, 123, 144, 147, 384], self.START, self.START, self.STEPS
        )
        self.assertEqual(
            frames,
            [(0, 0), (1, 1), (2, 2), (120, 120), (123, 123), (144, 144)],
        )

    def test_older_nwps_cycle_shifts_steps_forward(self):
        # NWPS ran 6 h before the GFS run: its step 6 is our hour 0, and its
        # coverage ends 6 h earlier on our timeline.
        cycle = self.START - dt.timedelta(hours=6)
        frames = nwps.select_frames([0, 1, 138, 139], self.START, cycle, self.STEPS)
        self.assertEqual(frames, [(0, 6), (1, 7), (138, 144)])

    def test_newer_nwps_cycle_drops_leading_hours(self):
        # NWPS ran 6 h after the GFS run: our first hours predate its
        # analysis and have no frame.
        cycle = self.START + dt.timedelta(hours=6)
        frames = nwps.select_frames([0, 5, 6, 7], self.START, cycle, self.STEPS)
        self.assertEqual(frames, [(6, 0), (7, 1)])

    def test_sub_hour_offset_yields_no_frames(self):
        cycle = self.START + dt.timedelta(minutes=30)
        self.assertEqual(
            nwps.select_frames([0, 1], self.START, cycle, self.STEPS), []
        )


class MosaicTests(unittest.TestCase):
    """Two synthetic domains: a coarse one spanning lon 0-10, and a finer
    one spanning lon 6-12 (overlap 6-10). Both span lat 0-4."""

    def make_mosaic(self):
        coarse = {
            "wfo": "aaa",
            "lat": np.linspace(0.0, 4.0, 5),  # 1.0 deg step, S->N like NWPS
            "lon": np.linspace(0.0, 10.0, 11),
        }
        fine = {
            "wfo": "bbb",
            "lat": np.linspace(0.0, 4.0, 9),  # 0.5 deg step
            "lon": np.linspace(6.0, 12.0, 13),
        }
        return coarse, fine, nwps._Mosaic([coarse, fine])

    def full_fields(self, domain, value):
        shape = (domain["lat"].size, domain["lon"].size)
        return np.full(shape, value, dtype=np.float32), np.zeros(shape, dtype=bool)

    def test_lattice_uses_finest_step_and_union_bbox(self):
        _, _, mosaic = self.make_mosaic()
        self.assertAlmostEqual(mosaic.lat[0], 4.0)  # north first
        self.assertAlmostEqual(mosaic.lat[-1], 0.0)
        self.assertAlmostEqual(mosaic.lon[0], 0.0)
        self.assertAlmostEqual(mosaic.lon[-1], 12.0)
        self.assertAlmostEqual(mosaic.lat[0] - mosaic.lat[1], 0.5)
        self.assertAlmostEqual(mosaic.lon[1] - mosaic.lon[0], 0.5)

    def test_overlap_blends_from_coarse_to_fine(self):
        coarse, fine, mosaic = self.make_mosaic()
        ch, cm = self.full_fields(coarse, 1.0)
        fh, fm = self.full_fields(fine, 2.0)
        grid, _ = mosaic.compose([("aaa", ch, cm), ("bbb", fh, fm)])
        lon_at = lambda lon: int(np.abs(mosaic.lon - lon).argmin())
        row = int(np.abs(mosaic.lat - 2.0).argmin())
        self.assertEqual(grid[row, lon_at(3.0)], 1.0)  # coarse only
        # Where the fine domain begins its values fade in over the feather
        # band instead of stepping from 1.0 to 2.0.
        at_edge = grid[row, lon_at(6.0)]
        mid_band = grid[row, lon_at(8.0)]
        self.assertAlmostEqual(float(at_edge), 1.0, places=5)
        self.assertGreater(float(mid_band), float(at_edge))
        self.assertLess(float(mid_band), 2.0)
        # Outside the coarse bbox there is nothing to blend with: pure fine.
        self.assertEqual(grid[row, lon_at(11.5)], 2.0)

    def test_fine_land_mask_overwrites_coarse_water(self):
        coarse, fine, mosaic = self.make_mosaic()
        ch, cm = self.full_fields(coarse, 1.0)
        fh, fm = self.full_fields(fine, 2.0)
        fm[:] = True  # the fine domain is all land
        grid, _ = mosaic.compose([("aaa", ch, cm), ("bbb", fh, fm)])
        row = int(np.abs(mosaic.lat - 2.0).argmin())
        overlap_col = int(np.abs(mosaic.lon - 8.0).argmin())
        self.assertTrue(np.isnan(grid[row, overlap_col]))
        self.assertEqual(grid[row, int(np.abs(mosaic.lon - 3.0).argmin())], 1.0)

    def test_alpha_feathers_edges_and_tracks_contributing_domains(self):
        coarse, fine, mosaic = self.make_mosaic()
        ch, cm = self.full_fields(coarse, 1.0)
        _, alpha = mosaic.compose([("aaa", ch, cm)])
        # Corner of the coarse bbox is on the coverage edge; far outside the
        # coarse bbox (fine-only lon > 10) there is no coverage at all.
        self.assertLess(alpha[0, 0], 1.0)
        self.assertGreater(alpha[0, 0], 0.0)
        self.assertEqual(alpha[0, -1], 0.0)
        # With both domains painted the same cell is covered.
        fh, fm = self.full_fields(fine, 2.0)
        _, alpha_both = mosaic.compose([("aaa", ch, cm), ("bbb", fh, fm)])
        self.assertGreater(alpha_both[0, -1], 0.0)


class PointsTests(unittest.TestCase):
    def test_write_points_skips_land_and_rounds(self):
        domain = {
            "lat": np.array([32.0, 32.5]),
            "lon": np.array([240.0, 240.5, 241.0]),
        }
        height = np.array([[1.234, 2.0, 3.0], [4.0, 5.0, 6.0]], dtype=np.float32)
        mask = np.zeros_like(height, dtype=bool)
        mask[0, 1] = True  # land cell
        fields = {
            "height": height,
            "mask": mask,
            "swell": np.full_like(height, 1.055),
            "period": np.full_like(height, 12.34),
            "direction": np.full_like(height, 359.6),
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "pts.geojson")
            count = nwps.write_nwps_points(domain, fields, path)
            with open(path) as f:
                doc = json.load(f)
            self.assertTrue(os.path.exists(path + ".gz"))
        self.assertEqual(count, 5)
        self.assertEqual(len(doc["features"]), 5)
        first = doc["features"][0]
        self.assertEqual(first["geometry"]["coordinates"], [240.0, 32.0])
        self.assertEqual(first["properties"]["h"], 1.23)
        self.assertEqual(first["properties"]["s"], 1.05)
        self.assertEqual(first["properties"]["p"], 12.3)
        self.assertEqual(first["properties"]["d"], 0)  # 360 wraps to 0
        lons = [f["geometry"]["coordinates"][0] for f in doc["features"]]
        self.assertNotIn(
            240.5, [lon for f, lon in zip(doc["features"], lons)
                    if f["geometry"]["coordinates"][1] == 32.0]
        )


class RenderAlphaTests(unittest.TestCase):
    def test_alpha_renders_rgba_with_feathered_edge(self):
        from PIL import Image

        lat = np.linspace(34.0, 33.0, 8)
        lon = np.linspace(240.0, 241.0, 8)
        lat2d, lon2d = np.meshgrid(lat, lon, indexing="ij")
        height = np.full((8, 8), 2.0, dtype=np.float32)
        height[0, 0] = np.nan  # one no-data cell stays fully transparent
        alpha = np.ones((8, 8), dtype=np.float32)
        alpha[:, -1] = 0.25  # feathered east edge
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "frame.png")
            bounds = gfs_to_contours.render_heatmap_png(
                {"lon": lon2d, "lat": lat2d, "height": height}, path, alpha=alpha
            )
            with Image.open(path) as img:
                self.assertEqual(img.mode, "RGBA")
                data = np.asarray(img)
        self.assertEqual(bounds["north"], 34.0)
        self.assertEqual(data[0, 0, 3], 0)  # NaN cell transparent
        self.assertEqual(data[-1, 0, 3], 255)  # interior opaque
        self.assertEqual(data[-1, -1, 3], 64)  # 0.25 * 255 rounded
        self.assertGreater(data[-1, -1, :3].sum(), 0)  # still colored


if __name__ == "__main__":
    unittest.main()
