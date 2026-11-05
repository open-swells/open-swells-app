import unittest
from datetime import UTC, datetime
from unittest.mock import Mock

import numpy as np

import fes_tides
from fes_tides import (
    find_extremes,
    find_wet_point,
    fes_spot_tides,
    model_mllw,
    site_id,
    still_current,
)

M2_S = 12.4206 * 3600
K1_S = 23.9345 * 3600


def harmonic(m2=0.8, k1=0.0, land_lon=None, land_to=180.0):
    """A predictor of M2 + K1, NaN (land) from land_lon east to land_to."""

    def predict(lons, lats, times):
        t = times.astype("datetime64[s]").astype(np.int64).astype(float)
        h = m2 * np.cos(2 * np.pi * t / M2_S) + k1 * np.cos(2 * np.pi * t / K1_S)
        if land_lon is not None:
            lons = np.asarray(lons)
            h = np.where((lons > land_lon) & (lons < land_to), np.nan, h)
        return h

    return predict


class FindExtremesTests(unittest.TestCase):
    def test_refines_turns_between_samples(self):
        # a 12 h cosine whose first high falls 7 minutes past a sample
        start, step, period = 0, 600, 12 * 3600
        t = start + np.arange(200) * step
        values = np.cos(2 * np.pi * (t - 3 * 3600 - 420) / period)
        events = find_extremes(start, step, values)
        self.assertEqual([e[2] for e in events[:3]], ["H", "L", "H"])
        self.assertLessEqual(abs(events[0][0] - (3 * 3600 + 420)), 30)
        self.assertAlmostEqual(events[0][1], 1.0, places=2)
        self.assertLessEqual(abs(events[1][0] - (9 * 3600 + 420)), 30)
        self.assertAlmostEqual(events[1][1], -1.0, places=2)

    def test_drops_wiggles_and_keeps_alternation(self):
        values = [0.0, 0.5, 1.0, 0.98, 1.0, 0.5, 0.0, -0.5, -0.48, -0.6, 0.0]
        events = find_extremes(0, 3600, values)
        self.assertEqual([e[2] for e in events], ["H", "L"])

    def test_flat_series_has_no_events(self):
        self.assertEqual(find_extremes(0, 600, [0.2] * 20), [])


class SiteTests(unittest.TestCase):
    def test_site_id_snaps_nearby_spots_together(self):
        self.assertEqual(site_id(-8.7201, 115.1689), site_id(-8.7149, 115.1711))
        self.assertEqual(site_id(-8.7201, 115.1689), "fes:-8.70,115.15")

    def test_find_wet_point_moves_off_land(self):
        now = datetime(2026, 7, 11, tzinfo=UTC)
        self.assertEqual(find_wet_point(harmonic(), 10.0, 20.0, now), (10.0, 20.0))
        # the site's point is land; the nearest wet probe is on the first
        # ring, about 5.5 km away and west of it
        lat, lon = find_wet_point(harmonic(land_lon=19.99), 10.0, 20.0, now)
        self.assertLess(lon, 19.99)
        self.assertAlmostEqual(fes_tides._haversine_km(10.0, 20.0, lat, lon), 5.5, delta=0.2)
        self.assertIsNone(find_wet_point(harmonic(land_lon=-180), 10.0, 20.0, now))

    def test_model_mllw_is_the_mean_lower_low(self):
        # a pure semidiurnal tide: every low is -amplitude
        self.assertAlmostEqual(model_mllw(harmonic(m2=0.8), 0, 0), -0.8, places=2)
        # a diurnal inequality makes the lower low deeper than either alone
        self.assertLess(model_mllw(harmonic(m2=0.8, k1=0.3), 0, 0), -0.85)


class FesSpotTideTests(unittest.TestCase):
    NOW = datetime(2026, 7, 11, 6, tzinfo=UTC)
    SPOTS = [
        {"id": "uluwatu", "lat": -8.815, "lon": 115.088},
        {"id": "padang", "lat": -8.812, "lon": 115.103},  # same site
        {"id": "inland", "lat": 10.0, "lon": 30.0},
    ]

    def factory(self):
        def predict_for(bbox):
            # 25E to 60E is land in this fake atlas
            return harmonic(m2=0.8, k1=0.2, land_lon=25.0, land_to=60.0)

        return Mock(side_effect=predict_for)

    def test_predicts_sites_shared_by_nearby_spots(self):
        sites = {}
        factory = self.factory()
        stations, matches, errors = fes_spot_tides(self.SPOTS, factory, now=self.NOW, sites=sites)

        self.assertEqual(errors, [])
        self.assertEqual(matches["uluwatu"]["station"], matches["padang"]["station"])
        self.assertNotIn("inland", matches)
        self.assertIsNone(sites[site_id(10.0, 30.0)])
        self.assertEqual(factory.call_count, 2)  # one tile each
        station = stations[matches["uluwatu"]["station"]]
        self.assertEqual((station["source"], station["datum"], station["type"]), ("FES2022", "FES MLLW", "M"))
        self.assertLess(matches["uluwatu"]["km"], 5)

        # a day before now's UTC day through a day past the forecast, hourly
        start = int(datetime(2026, 7, 10, tzinfo=UTC).timestamp())
        self.assertEqual(station["hourly"]["start"], start)
        self.assertEqual(len(station["hourly"]["v"]), 24 * 19 + 1)
        # heights are above the modeled MLLW: lows hover around zero
        lows = [e[1] for e in station["hilo"] if e[2] == "L"]
        self.assertAlmostEqual(float(np.mean([min(lows[i : i + 2]) for i in range(0, len(lows) - 1, 2)])), 0, delta=0.1)
        self.assertAlmostEqual(station["msl"], -model_mllw(harmonic(m2=0.8, k1=0.2), 0, 0), places=2)
        self.assertGreater(len(station["hilo"]), 60)  # ~4 a day for 18 days

    def test_reuses_cached_sites_and_todays_predictions(self):
        spots = self.SPOTS[:1]
        sites = {}
        stations, _, _ = fes_spot_tides(spots, self.factory(), now=self.NOW, sites=sites)

        # a known site skips the probe and datum: one prediction call
        predict = Mock(side_effect=harmonic(m2=0.8, k1=0.2))
        fes_spot_tides(spots, lambda bbox: predict, now=self.NOW, sites=sites)
        self.assertEqual(predict.call_count, 1)

        # today's predictions don't load the atlas at all
        factory = self.factory()
        again, matches, _ = fes_spot_tides(spots, factory, now=self.NOW, cached=stations, sites=sites)
        factory.assert_not_called()
        self.assertEqual(again, stations)
        self.assertIn("uluwatu", matches)

    def test_unreadable_atlas_keeps_previous_predictions(self):
        spots = self.SPOTS[:1]
        stations, _, _ = fes_spot_tides(spots, self.factory(), now=self.NOW)
        broken = Mock(side_effect=OSError("missing M2.nc"))
        tomorrow = self.NOW.replace(day=12)

        kept, matches, errors = fes_spot_tides(
            spots, broken, now=tomorrow, previous=still_current(stations, tomorrow)
        )
        self.assertEqual(kept, stations)
        self.assertIn("uluwatu", matches)
        self.assertIn("kept the previous prediction", errors[0]["error"])

        # with nothing to fall back on the spot just has no tide
        none, matches, errors = fes_spot_tides(spots, broken, now=tomorrow)
        self.assertEqual((none, matches), ({}, {}))
        self.assertIn("missing M2.nc", errors[0]["error"])

    def test_still_current_drops_stale_and_noaa_stations(self):
        now = datetime(2026, 7, 11, tzinfo=UTC)
        t = now.timestamp()
        stations = {
            "fes:1.00,2.00": {"hilo": [[t + 10 * 86400, 1.0, "H"]]},
            "fes:3.00,4.00": {"hilo": [[t + 3600, 1.0, "H"]]},
            "9410230": {"hilo": [[t + 10 * 86400, 1.0, "H"]]},
        }
        self.assertEqual(list(still_current(stations, now)), ["fes:1.00,2.00"])


class ResolveConfigTests(unittest.TestCase):
    def test_unset_uses_the_default_only_when_it_exists(self):
        from unittest.mock import patch

        with patch("fes_tides.os.path.exists", return_value=True):
            self.assertEqual(fes_tides.resolve_config(None), fes_tides.DEFAULT_FES_CONFIG)
            self.assertIsNone(fes_tides.resolve_config(""))  # explicitly off
            self.assertEqual(fes_tides.resolve_config("/atlas/a.yml"), "/atlas/a.yml")
        with patch("fes_tides.os.path.exists", return_value=False):
            self.assertIsNone(fes_tides.resolve_config(None))


class SiteCacheTests(unittest.TestCase):
    def test_round_trip_and_model_change(self):
        import os
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "cache", fes_tides.FES_SITES_NAME)
            sites = {"fes:1.00,2.00": {"lat": 1.0, "lon": 2.0, "msl": 0.9}, "fes:3.00,4.00": None}
            fes_tides.save_site_cache(path, "/atlas/a.yml", sites)
            self.assertEqual(fes_tides.load_site_cache(path, "/atlas/a.yml"), sites)
            self.assertEqual(fes_tides.load_site_cache(path, "/atlas/b.yml"), {})
        self.assertEqual(fes_tides.load_site_cache(None, "/atlas/a.yml"), {})


if __name__ == "__main__":
    unittest.main()
