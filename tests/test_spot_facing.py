import json
import os
import struct
import tempfile
import unittest

import numpy as np

import spot_facing
from spot_facing import (
    Coastline,
    apply_result,
    arc_contains,
    compute_spot,
    open_arcs,
    read_gshhg,
)


def box(west, south, east, north):
    """Closed (lon, lat) ring for a rectangle."""
    return np.array(
        [(west, south), (east, south), (east, north), (west, north), (west, south)],
        dtype=np.float64,
    )


def ang_diff(a, b):
    d = abs(a - b) % 360.0
    return 360.0 - d if d > 180.0 else d


def arc_width(arc):
    return (arc[1] - arc[0]) % 360.0 or 360.0


# Fixture shorelines sit off whole degrees: those are tile edges, where
# clipping drops a line lying exactly on the boundary (real shorelines
# never run exactly along one).
# Land north of lat 0.3: a straight, south-facing shoreline.
MAINLAND = box(-5.0, 0.3, 5.0, 3.3)
# A spot on the beach, ~50 m inland of the waterline.
BEACH = (0.30045, 0.03)


class ComputeSpotTests(unittest.TestCase):
    def test_straight_coast_faces_the_sea(self):
        result = compute_spot(Coastline([MAINLAND]), *BEACH)

        self.assertEqual(result["status"], "beach")
        self.assertLess(ang_diff(result["facing"], 180.0), 2.0)
        self.assertLess(result["snap_m"], 100.0)
        # One arc: the open half-plane, centered on the facing.
        self.assertEqual(len(result["arcs"]), 1)
        self.assertTrue(arc_contains(result["arcs"][0], 180.0))
        self.assertAlmostEqual(arc_width(result["arcs"][0]), 180.0, delta=12.0)
        self.assertFalse(arc_contains(result["arcs"][0], 0.0))
        self.assertEqual(result["flags"], [])

    def test_island_splits_the_window(self):
        # ~22 km wide island 50 km due south shadows the middle of the
        # window: the beach keeps both open sides instead of losing one.
        island = box(-0.07, -0.2, 0.13, -0.15)
        result = compute_spot(Coastline([MAINLAND, island]), *BEACH)

        self.assertEqual(result["status"], "beach")
        self.assertLess(ang_diff(result["facing"], 180.0), 2.0)
        self.assertEqual(len(result["arcs"]), 2)
        self.assertFalse(any(arc_contains(arc, 180.0) for arc in result["arcs"]))
        self.assertTrue(any(arc_contains(arc, 120.0) for arc in result["arcs"]))
        self.assertTrue(any(arc_contains(arc, 240.0) for arc in result["arcs"]))
        self.assertIn("facing_outside_window", result["flags"])

    def test_small_islet_is_bridged(self):
        # A 1 km rock 50 km out blocks one ray at most; swell wraps around it.
        rock = box(0.0255, -0.15, 0.0345, -0.141)
        result = compute_spot(Coastline([MAINLAND, rock]), *BEACH)

        self.assertEqual(len(result["arcs"]), 1)
        self.assertTrue(arc_contains(result["arcs"][0], 180.0))

    def test_sea_side_chosen_on_a_west_coast(self):
        # Land to the east of lon 0.3: the beach faces west.
        land = box(0.3, -3.0, 3.3, 3.0)
        result = compute_spot(Coastline([land]), 0.03, 0.3004)

        self.assertLess(ang_diff(result["facing"], 270.0), 2.0)

    def test_inland_spot_is_skipped(self):
        result = compute_spot(Coastline([MAINLAND]), 1.5, 0.03)
        self.assertEqual(result["status"], "inland")

    def test_offshore_reef_gets_a_window_from_itself(self):
        # 55 km offshore: no shoreline within snapping range.
        result = compute_spot(Coastline([MAINLAND]), -0.2, 0.03)

        self.assertEqual(result["status"], "offshore")
        self.assertEqual(len(result["arcs"]), 1)
        # Everything but the mainland to the north is open.
        self.assertFalse(arc_contains(result["arcs"][0], 0.0))
        self.assertTrue(arc_contains(result["arcs"][0], 180.0))

    def test_coast_across_the_dateline(self):
        # GSHHG keeps a dateline-crossing polygon's longitudes past 180.
        land = box(179.0, 0.3, 181.0, 3.3)
        coastline = Coastline([land])
        for lon in (179.9, -179.9):
            result = compute_spot(coastline, BEACH[0], lon)
            self.assertEqual(result["status"], "beach", lon)
            self.assertLess(ang_diff(result["facing"], 180.0), 2.0, lon)


class OpenArcsTests(unittest.TestCase):
    step = 3.0
    bearings = np.arange(0.0, 360.0, 3.0)

    def distances(self, blocked):
        d = np.full(len(self.bearings), 150_000.0)
        for start, end in blocked:  # inclusive bearings
            d[(self.bearings >= start) & (self.bearings <= end)] = 1_000.0
        return d

    def test_arc_wraps_through_north(self):
        arcs = open_arcs(self.bearings, self.distances([(60, 300)]), self.step)
        self.assertEqual(len(arcs), 1)
        self.assertTrue(arc_contains(arcs[0], 0.0))
        self.assertAlmostEqual(arcs[0][0], 301.5)
        self.assertAlmostEqual(arcs[0][1], 58.5)

    def test_narrow_gaps_bridged_wide_gaps_split(self):
        # 6 deg gap (bridged) and a 30 deg gap (splits): two arcs, widest first.
        arcs = open_arcs(
            self.bearings, self.distances([(0, 90), (150, 153), (240, 270)]), self.step
        )
        self.assertEqual(len(arcs), 2)
        self.assertTrue(arc_contains(arcs[0], 150.0))  # bridged into the wide arc
        self.assertGreater(arc_width(arcs[0]), arc_width(arcs[1]))

    def test_all_open_and_all_blocked(self):
        self.assertEqual(open_arcs(self.bearings, self.distances([]), self.step), [(0.0, 360.0)])
        # Enclosed water: the bar drops to a share of the longest ray, so
        # the widest water still reads as open.
        d = np.full(len(self.bearings), 2_000.0)
        d[(self.bearings >= 90) & (self.bearings <= 120)] = 20_000.0
        arcs = open_arcs(self.bearings, d, self.step)
        self.assertEqual(len(arcs), 1)
        self.assertTrue(arc_contains(arcs[0], 105.0))

    def test_lone_narrow_slot_is_kept(self):
        d = np.full(len(self.bearings), 1_000.0)
        d[self.bearings == 75.0] = 150_000.0
        arcs = open_arcs(self.bearings, d, self.step)
        self.assertEqual(arcs, [(73.5, 76.5)])


class ReadGshhgTests(unittest.TestCase):
    def write(self, polygons):
        """polygons: (level, greenwich, west_udeg, [(x_udeg, y_udeg), ...])"""
        chunks = []
        for pid, (level, greenwich, west, points) in enumerate(polygons):
            flag = level | (12 << 8) | (greenwich << 16)
            chunks.append(struct.pack(">11i", pid, len(points), flag, west, 0, 0, 0, 0, 0, -1, -1))
            for x, y in points:
                chunks.append(struct.pack(">2i", x, y))
        f = tempfile.NamedTemporaryFile(delete=False, suffix=".b")
        f.write(b"".join(chunks))
        f.close()
        self.addCleanup(os.remove, f.name)
        return f.name

    def test_levels_and_longitude_convention(self):
        path = self.write([
            # plain land polygon
            (1, 0, 10_000_000, [(10_000_000, 1_000_000), (11_000_000, 1_000_000), (10_500_000, 2_000_000)]),
            # lake: skipped
            (2, 0, 10_000_000, [(10_000_000, 1_000_000), (10_100_000, 1_000_000), (10_050_000, 1_100_000)]),
            # western hemisphere stored as 0..360: shifted to negative
            (1, 0, 200_000_000, [(200_000_000, 0), (201_000_000, 0), (200_500_000, 1_000_000)]),
            # crosses Greenwich: points past 270 E are west of it
            (1, 1, -1_000_000, [(359_000_000, 0), (1_000_000, 0), (0, 1_000_000)]),
        ])
        polygons = read_gshhg(path)

        self.assertEqual(len(polygons), 3)
        np.testing.assert_allclose(polygons[0][1], (11.0, 1.0))
        np.testing.assert_allclose(polygons[1][:, 0], (-160.0, -159.0, -159.5))
        np.testing.assert_allclose(polygons[2][:, 0], (-1.0, 1.0, 0.0))


class ApplyResultTests(unittest.TestCase):
    def test_writes_rounded_facing_and_arcs(self):
        spot = {"id": "a", "lat": 0, "lon": 0}
        apply_result(spot, {"facing": 359.6, "arcs": [(301.5, 58.5), (90.4, 100.2)]})
        self.assertEqual(spot["facing"], 0)
        self.assertEqual(spot["open_water"], [[302, 58], [90, 100]])

    def test_open_all_around(self):
        spot = {"id": "a"}
        apply_result(spot, {"facing": 10.0, "arcs": [(0.0, 360.0)]})
        self.assertEqual(spot["open_water"], [[0, 360]])

    def test_failed_spot_clears_stale_values(self):
        spot = {"id": "a", "facing": 90, "open_water": [[0, 180]]}
        apply_result(spot, {"status": "inland"})
        self.assertNotIn("facing", spot)
        self.assertNotIn("open_water", spot)

    def test_hand_tuned_window_is_untouched(self):
        spot = {"id": "a", "swell_window": [170, 260]}
        apply_result(spot, {"facing": 223.0, "arcs": [(133.5, 214.5)]})
        self.assertEqual(spot, {"id": "a", "swell_window": [170, 260]})


class FormatSpotsTests(unittest.TestCase):
    def test_number_arrays_inline_everything_else_indented(self):
        spots = [{"name": "Plage é", "lat": -1.5, "swell_window": [170, 260],
                  "open_water": [[184, 332], [70, 76]], "tags": ["a", "b"]}]
        text = spot_facing.format_spots(spots)
        self.assertIn('    "swell_window": [170, 260],\n', text)
        self.assertIn('    "open_water": [[184, 332], [70, 76]],\n', text)
        self.assertIn('    "name": "Plage é",\n', text)
        self.assertIn('    "tags": [\n      "a",', text)  # strings untouched
        self.assertEqual(json.loads(text), spots)


class CliTests(unittest.TestCase):
    def test_updates_every_file_and_keeps_formatting(self):
        with tempfile.TemporaryDirectory() as tmp:
            gshhg = os.path.join(tmp, "coast.b")
            ring = MAINLAND[:-1]
            with open(gshhg, "wb") as f:
                f.write(struct.pack(">11i", 0, len(ring), 1, -5_000_000, 0, 0, 0, 0, 0, -1, -1))
                for lon, lat in ring:
                    f.write(struct.pack(">2i", int(lon * 1e6), int(lat * 1e6)))
            spots = '[\n  {\n    "name": "Plage é",\n    "lat": %s,\n    "lon": %s,\n    "id": "a"\n  }\n]\n' % BEACH
            paths = [os.path.join(tmp, "one.json"), os.path.join(tmp, "two.json")]
            for path in paths:
                with open(path, "w", encoding="utf-8") as f:
                    f.write(spots)

            self.assertEqual(spot_facing.main(["--gshhg", gshhg, "--workers", "1", *paths]), 0)

            for path in paths:
                with open(path, encoding="utf-8") as f:
                    text = f.read()
                self.assertIn('"name": "Plage é"', text)  # not \\u-escaped
                self.assertIn('"facing": 180', text)
                self.assertRegex(text, r'"open_water": \[\[\d+, \d+\]\]')
                self.assertTrue(text.endswith("]\n"))


if __name__ == "__main__":
    unittest.main()
