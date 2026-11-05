import gzip
import json
import os
import tempfile
import unittest
from datetime import UTC, datetime
from unittest.mock import Mock, patch

from tides import (
    DATA_URL,
    fetch_hilo,
    fetch_hourly,
    fetch_station,
    match_spots,
    write_spot_tides,
    write_tides,
)

STATIONS = [
    {"id": "near", "name": "Near", "lat": 33.00, "lon": -117.30, "type": "S"},
    {"id": "far", "name": "Far", "lat": 33.50, "lon": -117.80, "type": "R"},
]


def harmonic(m2=0.8, k1=0.0):
    """A stand-in FES predictor: M2 + K1 everywhere."""
    import numpy as np

    def predict(lons, lats, times):
        t = times.astype("datetime64[s]").astype(np.int64).astype(float)
        return m2 * np.cos(2 * np.pi * t / (12.4206 * 3600)) + k1 * np.cos(2 * np.pi * t / (23.9345 * 3600))

    return predict


class FakeResponse:
    def __init__(self, payload):
        self.payload = payload

    def raise_for_status(self):
        return None

    def json(self):
        return self.payload


class TideTests(unittest.TestCase):
    def test_fetch_station_combines_predictions_observations_and_metadata(self):
        session = Mock()
        session.get.side_effect = [
            FakeResponse({"predictions": [{"t": "2026-07-11 00:00", "v": "1.2"}]}),
            FakeResponse({"data": [{"t": "2026-07-11 00:00", "v": "1.3"}]}),
            FakeResponse({"stations": [{"name": "Test", "lat": 1.0, "lng": -2.0}]}),
        ]

        result = fetch_station(
            session,
            "1234567",
            now=datetime(2026, 7, 11, tzinfo=UTC),
        )

        self.assertEqual(result["name"], "Test")
        self.assertEqual(result["datum"], "MLLW")
        self.assertEqual(len(result["predictions"]), 1)
        self.assertEqual(len(result["observations"]), 1)
        prediction_call = session.get.call_args_list[0]
        self.assertEqual(prediction_call.args[0], DATA_URL)
        self.assertEqual(prediction_call.kwargs["params"]["interval"], "h")

    def test_write_tides_keeps_other_stations_when_one_fails(self):
        session = Mock()
        with tempfile.NamedTemporaryFile() as output:
            from unittest.mock import patch

            with patch(
                "tides.fetch_station",
                side_effect=[{"id": "good"}, RuntimeError("unavailable")],
            ):
                successes, failures = write_tides(
                    session,
                    ["good", "bad"],
                    output.name,
                    now=datetime(2026, 7, 11, tzinfo=UTC),
                )
            output.seek(0)
            payload = json.load(output)

        self.assertEqual((successes, failures), (1, 1))
        self.assertEqual(payload["stations"], [{"id": "good"}])
        self.assertEqual(payload["errors"][0]["station"], "bad")


class SpotTideTests(unittest.TestCase):
    def test_match_spots_picks_nearest_station_within_cutoff(self):
        spots = [
            {"id": "a", "lat": 33.01, "lon": -117.29},
            {"id": "remote", "lat": 20.0, "lon": -140.0},
        ]
        matches = match_spots(spots, STATIONS, max_km=50)
        self.assertEqual(matches["a"]["station"], "near")
        self.assertLess(matches["a"]["km"], 2)
        self.assertNotIn("remote", matches)

    def test_match_spots_prefers_hand_set_station(self):
        spots = [
            {"id": "pinned", "lat": 33.01, "lon": -117.29, "tide_station": "far"},
            {"id": "retired", "lat": 33.01, "lon": -117.29, "tide_station": "gone"},
        ]
        matches = match_spots(spots, STATIONS, max_km=50)
        self.assertEqual(matches["pinned"]["station"], "far")
        # a pinned station CO-OPS no longer predicts falls back to nearest
        self.assertEqual(matches["retired"]["station"], "near")

    def test_fetch_hilo_parses_events_to_unix_seconds(self):
        session = Mock()
        session.get.return_value = FakeResponse(
            {"predictions": [{"t": "2026-07-11 02:38", "v": "1.234", "type": "H"}]}
        )
        events = fetch_hilo(session, "near", now=datetime(2026, 7, 11, tzinfo=UTC))
        self.assertEqual(events, [[int(datetime(2026, 7, 11, 2, 38, tzinfo=UTC).timestamp()), 1.234, "H"]])
        params = session.get.call_args.kwargs["params"]
        self.assertEqual(params["interval"], "hilo")
        self.assertEqual(params["begin_date"], "20260710")

    def test_fetch_hourly_requires_contiguous_hours(self):
        session = Mock()
        session.get.return_value = FakeResponse(
            {"predictions": [{"t": "2026-07-11 00:00", "v": "0.5"}, {"t": "2026-07-11 01:00", "v": "0.61"}]}
        )
        hourly = fetch_hourly(session, "far", now=datetime(2026, 7, 11, tzinfo=UTC))
        self.assertEqual(hourly, {"start": int(datetime(2026, 7, 11, tzinfo=UTC).timestamp()), "v": [0.5, 0.61]})
        self.assertEqual(session.get.call_args.kwargs["params"]["interval"], "h")

        session.get.return_value = FakeResponse(
            {"predictions": [{"t": "2026-07-11 00:00", "v": "0.5"}, {"t": "2026-07-11 02:00", "v": "0.6"}]}
        )
        with self.assertRaises(RuntimeError):
            fetch_hourly(session, "far", now=datetime(2026, 7, 11, tzinfo=UTC))

    def test_write_spot_tides_adds_hourly_for_reference_stations_only(self):
        spots = [
            {"id": "a", "lat": 33.01, "lon": -117.29},
            {"id": "b", "lat": 33.49, "lon": -117.79},
        ]
        with tempfile.TemporaryDirectory() as directory:
            spots_path = os.path.join(directory, "spots.json")
            out_path = os.path.join(directory, "spot_tides.json.gz")
            with open(spots_path, "w") as f:
                json.dump(spots, f)
            hourly = Mock(return_value={"start": 0, "v": [0.1]})
            with patch("tides.fetch_prediction_stations", return_value=STATIONS), patch(
                "tides.fetch_hilo", return_value=[[1, 0.5, "L"]]
            ), patch("tides.fetch_hourly", hourly):
                write_spot_tides(Mock(), spots_path, out_path, now=datetime(2026, 7, 11, tzinfo=UTC))
            with gzip.open(out_path, "rt") as f:
                payload = json.load(f)

        self.assertNotIn("hourly", payload["stations"]["near"])  # subordinate
        self.assertEqual(payload["stations"]["far"]["hourly"], {"start": 0, "v": [0.1]})
        self.assertEqual(hourly.call_args.args[1], "far")

    def test_write_spot_tides_drops_spots_of_failed_stations(self):
        spots = [
            {"id": "a", "lat": 33.01, "lon": -117.29},
            {"id": "b", "lat": 33.49, "lon": -117.79},
        ]

        def hilo(session, station, now):
            if station == "far":
                raise RuntimeError("unavailable")
            return [[1, 0.5, "L"]]

        with tempfile.TemporaryDirectory() as directory:
            spots_path = os.path.join(directory, "spots.json")
            out_path = os.path.join(directory, "spot_tides.json.gz")
            with open(spots_path, "w") as f:
                json.dump(spots, f)
            with patch("tides.fetch_prediction_stations", return_value=STATIONS), patch(
                "tides.fetch_hilo", side_effect=hilo
            ), patch("tides.fetch_hourly", return_value=None):
                result = write_spot_tides(Mock(), spots_path, out_path, now=datetime(2026, 7, 11, tzinfo=UTC))
            with gzip.open(out_path, "rt") as f:
                payload = json.load(f)

        self.assertEqual(result, (1, 1))
        self.assertEqual(list(payload["stations"]), ["near"])
        self.assertEqual(payload["stations"]["near"]["hilo"], [[1, 0.5, "L"]])
        self.assertEqual(list(payload["spots"]), ["a"])
        self.assertEqual(payload["errors"][0]["station"], "far")

    def _write(self, directory, spots, **kwargs):
        spots_path = os.path.join(directory, "spots.json")
        out_path = os.path.join(directory, "spot_tides.json.gz")
        with open(spots_path, "w") as f:
            json.dump(spots, f)
        with patch("tides.fetch_prediction_stations", return_value=STATIONS):
            result = write_spot_tides(Mock(), spots_path, out_path, **kwargs)
        with gzip.open(out_path, "rt") as f:
            return result, json.load(f)

    def test_write_spot_tides_reuses_same_day_cache(self):
        spots = [{"id": "a", "lat": 33.01, "lon": -117.29}]
        with tempfile.TemporaryDirectory() as directory:
            cache = os.path.join(directory, "cache", "spot_tides.json.gz")
            hilo = Mock(return_value=[[1, 0.5, "L"]])
            with patch("tides.fetch_hilo", hilo), patch("tides.fetch_hourly", return_value=None):
                morning = datetime(2026, 7, 11, 6, tzinfo=UTC)
                self._write(directory, spots, now=morning, cache_path=cache)
                self._write(directory, spots, now=morning.replace(hour=18), cache_path=cache)
                self.assertEqual(hilo.call_count, 1)
                # a new UTC day refetches
                _, payload = self._write(directory, spots, now=datetime(2026, 7, 12, tzinfo=UTC), cache_path=cache)
                self.assertEqual(hilo.call_count, 2)
        self.assertEqual(payload["stations"]["near"]["hilo"], [[1, 0.5, "L"]])

    def test_write_spot_tides_stops_fetching_when_rate_limited(self):
        import requests

        spots = [
            {"id": "a", "lat": 33.01, "lon": -117.29},
            {"id": "b", "lat": 33.49, "lon": -117.79},
        ]
        response = Mock(status_code=403)
        hilo = Mock(side_effect=requests.HTTPError("403 Forbidden", response=response))
        with tempfile.TemporaryDirectory() as directory:
            with patch("tides.fetch_hilo", hilo), patch("tides.fetch_hourly", return_value=None):
                result, payload = self._write(directory, spots, now=datetime(2026, 7, 11, tzinfo=UTC), workers=1)
        self.assertEqual(result, (0, 2))
        self.assertEqual(hilo.call_count, 1)
        self.assertIn("rate limiting", payload["errors"][1]["error"])

    def test_write_spot_tides_falls_back_to_fes_only_without_a_station(self):
        spots = [
            {"id": "a", "lat": 33.01, "lon": -117.29},  # 1 km from "near"
            {"id": "bali", "lat": -8.815, "lon": 115.088},
        ]
        factory = Mock(side_effect=lambda bbox: harmonic(m2=0.8, k1=0.2))
        with tempfile.TemporaryDirectory() as directory:
            cache = os.path.join(directory, "cache", "spot_tides.json.gz")
            with patch("tides.fetch_hilo", return_value=[[1, 0.5, "L"]]), patch(
                "tides.fetch_hourly", return_value=None
            ), patch("fes_tides.pyfes_predictors", return_value=factory):
                result, payload = self._write(
                    directory, spots, now=datetime(2026, 7, 11, tzinfo=UTC), cache_path=cache, fes_config="fes.yml"
                )
            self.assertTrue(os.path.exists(os.path.join(directory, "cache", "fes_sites.json")))

        self.assertEqual(result, (2, 0))
        self.assertEqual(payload["spots"]["a"]["station"], "near")
        near = payload["stations"]["near"]
        self.assertEqual((near["source"], near["datum"]), ("NOAA CO-OPS", "MLLW"))
        fes_id = payload["spots"]["bali"]["station"]
        self.assertTrue(fes_id.startswith("fes:"))
        fes = payload["stations"][fes_id]
        self.assertEqual((fes["source"], fes["datum"], fes["type"]), ("FES2022", "FES MLLW", "M"))
        self.assertIn("msl", fes)
        self.assertTrue(fes["hilo"] and fes["hourly"]["v"])
        # only the spot without a station asked the model
        self.assertEqual(factory.call_count, 1)

    def test_write_spot_tides_without_fes_config_skips_the_model(self):
        spots = [{"id": "bali", "lat": -8.815, "lon": 115.088}]
        with tempfile.TemporaryDirectory() as directory:
            with patch("fes_tides.pyfes_predictors") as predictors:
                _, payload = self._write(directory, spots, now=datetime(2026, 7, 11, tzinfo=UTC))
        predictors.assert_not_called()
        self.assertEqual(payload["spots"], {})

    def test_write_spot_tides_keeps_previous_fes_when_pyfes_is_missing(self):
        spots = [{"id": "bali", "lat": -8.815, "lon": 115.088}]
        with tempfile.TemporaryDirectory() as directory:
            cache = os.path.join(directory, "cache", "spot_tides.json.gz")
            with patch("fes_tides.pyfes_predictors", return_value=lambda bbox: harmonic()):
                _, first = self._write(
                    directory, spots, now=datetime(2026, 7, 11, tzinfo=UTC), cache_path=cache, fes_config="fes.yml"
                )
            with patch("fes_tides.pyfes_predictors", side_effect=ImportError("No module named 'pyfes'")):
                _, second = self._write(
                    directory, spots, now=datetime(2026, 7, 12, tzinfo=UTC), cache_path=cache, fes_config="fes.yml"
                )
        self.assertEqual(second["spots"], first["spots"])
        self.assertEqual(second["stations"], first["stations"])
        self.assertIn("kept the previous prediction", second["errors"][0]["error"])


if __name__ == "__main__":
    unittest.main()
