import datetime as dt
import unittest

import numpy as np

from composite import composite_swell, composite_wind

VALID_DATE = dt.datetime(2026, 7, 13, tzinfo=dt.UTC)

# Native axes of the two NOAA global wave grids.
HI_LAT = np.linspace(52.5, -15.0, 406)
HI_LON = np.arange(2160) / 6.0
LO_LAT = np.linspace(90.0, -90.0, 721)
LO_LON = np.arange(1440) * 0.25


def make_swell_data(lat_axis, lon_axis, value):
    lon_grid, lat_grid = np.meshgrid(
        lon_axis.astype(np.float32), lat_axis.astype(np.float32)
    )
    shape = lat_grid.shape
    return {
        "lon": lon_grid,
        "lat": lat_grid,
        "height": np.full(shape, value, np.float32),
        "height_mask": np.zeros(shape, bool),
        "period": np.full(shape, value + 10.0, np.float32),
        "direction": np.full(shape, 90.0, np.float32),
        "swell_partitions": [
            {
                "sequence": sequence,
                "height": np.full(shape, value + sequence, np.float32),
                "period": np.full(shape, value + 10.0, np.float32),
                "direction": np.full(shape, 90.0, np.float32),
                "mask": np.zeros(shape, bool),
            }
            for sequence in (1, 2, 3)
        ],
        "valid_date": VALID_DATE,
    }


def make_wind_data(lat_axis, lon_axis, value):
    lon_grid, lat_grid = np.meshgrid(
        lon_axis.astype(np.float32), lat_axis.astype(np.float32)
    )
    shape = lat_grid.shape
    return {
        "lon": lon_grid,
        "lat": lat_grid,
        "speed": np.full(shape, value, np.float32),
        "direction": np.full(shape, 180.0, np.float32),
        "u": np.full(shape, value, np.float32),
        "v": np.full(shape, -value, np.float32),
        "mask": np.zeros(shape, bool),
        "valid_date": VALID_DATE,
    }


class CompositeSwellTests(unittest.TestCase):
    def test_band_from_fine_grid_and_poles_from_coarse_grid(self):
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)

        combined = composite_swell(hi, lo)

        lat_axis = combined["lat"][:, 0]
        self.assertEqual(combined["height"].shape, (1021, 2160))
        self.assertAlmostEqual(float(lat_axis[0]), 85.0, places=4)
        self.assertAlmostEqual(float(lat_axis[-1]), -85.0, places=4)
        self.assertAlmostEqual(float(combined["lon"][0, -1]), 359.8333, places=3)

        in_band = (lat_axis >= -15.0) & (lat_axis <= 52.5)
        np.testing.assert_array_equal(combined["height"][in_band], 2.0)
        np.testing.assert_array_equal(combined["height"][~in_band], 5.0)
        # Partition arrays follow the same split; sequence offsets survive.
        np.testing.assert_array_equal(
            combined["swell_partitions"][2]["height"][in_band], 5.0
        )
        np.testing.assert_array_equal(
            combined["swell_partitions"][2]["height"][~in_band], 8.0
        )
        self.assertEqual(combined["height_mask"].dtype, np.bool_)
        self.assertEqual(combined["height"].dtype, np.float32)

    def test_fully_masked_edge_rows_fall_back_to_coarse_grid(self):
        # The real 0p16 product pads its band edges with all-masked rows;
        # those must come from the coarse grid, not punch transparent seams.
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        hi["height_mask"][:2] = True  # 52.5N and the row below
        hi["height_mask"][-15:] = True  # southern edge padding

        combined = composite_swell(hi, lo)

        lat_axis = combined["lat"][:, 0]
        self.assertFalse(combined["height_mask"].any())
        # lat_axis is float32; use a tolerance well below the 1/6 deg step.
        data_band = (lat_axis >= HI_LAT[-16] - 1e-3) & (lat_axis <= HI_LAT[2] + 1e-3)
        np.testing.assert_array_equal(combined["height"][data_band], 2.0)
        np.testing.assert_array_equal(combined["height"][~data_band], 5.0)

    def test_fully_masked_fine_grid_uses_coarse_grid_everywhere(self):
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        hi["height_mask"][:] = True

        combined = composite_swell(hi, lo)

        np.testing.assert_array_equal(combined["height"], 5.0)
        self.assertFalse(combined["height_mask"].any())

    def test_wind_sea_follows_the_band_split(self):
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        for data, value in ((hi, 0.3), (lo, 0.9)):
            shape = data["height"].shape
            data["wind_sea"] = {
                "height": np.full(shape, value, np.float32),
                "period": np.full(shape, 4.0, np.float32),
                "direction": np.full(shape, 250.0, np.float32),
            }

        combined = composite_swell(hi, lo)

        lat_axis = combined["lat"][:, 0]
        in_band = (lat_axis >= -15.0) & (lat_axis <= 52.5)
        np.testing.assert_array_equal(combined["wind_sea"]["height"][in_band], np.float32(0.3))
        np.testing.assert_array_equal(combined["wind_sea"]["height"][~in_band], np.float32(0.9))
        self.assertEqual(combined["wind_sea"]["period"].shape, combined["height"].shape)

    def test_wind_sea_dropped_when_one_grid_lacks_it(self):
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        shape = hi["height"].shape
        hi["wind_sea"] = {
            key: np.zeros(shape, np.float32) for key in ("height", "period", "direction")
        }
        self.assertIsNone(composite_swell(hi, lo)["wind_sea"])

    def test_single_grid_passes_through_unchanged(self):
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        self.assertIs(composite_swell(None, lo), lo)
        self.assertIs(composite_swell(lo, None), lo)
        with self.assertRaises(ValueError):
            composite_swell(None, None)

    def test_mismatched_valid_dates_raise(self):
        hi = make_swell_data(HI_LAT, HI_LON, 2.0)
        lo = make_swell_data(LO_LAT, LO_LON, 5.0)
        lo["valid_date"] = VALID_DATE + dt.timedelta(hours=3)
        with self.assertRaises(ValueError):
            composite_swell(hi, lo)


class CompositeWindTests(unittest.TestCase):
    def test_band_from_fine_grid_and_poles_from_coarse_grid(self):
        hi = make_wind_data(HI_LAT, HI_LON, 7.0)
        lo = make_wind_data(LO_LAT, LO_LON, 12.0)

        combined = composite_wind(hi, lo)

        lat_axis = combined["lat"][:, 0]
        in_band = (lat_axis >= -15.0) & (lat_axis <= 52.5)
        np.testing.assert_array_equal(combined["speed"][in_band], 7.0)
        np.testing.assert_array_equal(combined["speed"][~in_band], 12.0)
        np.testing.assert_array_equal(combined["v"][~in_band], -12.0)
        self.assertEqual(combined["mask"].dtype, np.bool_)


if __name__ == "__main__":
    unittest.main()
