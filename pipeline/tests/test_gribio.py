import unittest
from unittest.mock import patch

import numpy as np

import gribio
import wind


class CountingMessage:
    """A pygrib message stand-in that counts its (expensive) decodes."""

    def __init__(self, values, valid_date="t0"):
        self._values = values
        self.validDate = valid_date
        self.decodes = 0
        self.lattices = 0

    @property
    def values(self):
        self.decodes += 1
        return self._values

    def __getitem__(self, key):
        return {"Ni": 2, "Nj": 2, "gridType": "regular_ll"}.get(key, 0.0)

    def latlons(self):
        self.lattices += 1
        return (
            np.array([[1.0, 1.0], [0.0, 0.0]]),
            np.array([[10.0, 11.0], [10.0, 11.0]]),
        )


class DecodeTests(unittest.TestCase):
    def test_values_and_mask_come_from_one_decode(self):
        message = CountingMessage(
            np.ma.array([[1.5, 2.0], [3.0, 4.0]], mask=[[False, True], [False, False]])
        )
        values, mask = gribio.decode(message)
        self.assertEqual(message.decodes, 1)
        self.assertEqual(values.dtype, np.float32)
        self.assertTrue(np.isnan(values[0, 1]))
        self.assertEqual(float(values[0, 0]), 1.5)
        np.testing.assert_array_equal(mask, [[False, True], [False, False]])

    def test_unmasked_field_has_an_all_false_mask(self):
        values, mask = gribio.decode(CountingMessage(np.ones((2, 2))))
        self.assertEqual(mask.shape, (2, 2))
        self.assertFalse(mask.any())
        self.assertEqual(values.dtype, np.float32)


class FakeGribFile:
    def __init__(self, messages):
        self.messages = messages

    def select(self, *, name):
        return [self.messages[name]]

    def close(self):
        pass


class ExtractWindTests(unittest.TestCase):
    def test_each_field_is_decoded_once_and_the_lattice_is_shared(self):
        def grib():
            field = np.ma.array([[1.0, 2.0], [3.0, 4.0]], mask=[[False, False], [True, False]])
            return {
                name: CountingMessage(field)
                for name in (
                    "Wind speed", "Wind direction",
                    "U component of wind", "V component of wind",
                )
            }

        gribio.GRID_CACHE.clear()
        self.addCleanup(gribio.GRID_CACHE.clear)
        first, second = grib(), grib()
        with patch.object(wind.pygrib, "open", side_effect=[FakeGribFile(first), FakeGribFile(second)]):
            data = wind.extract_wind("a.grib2")
            again = wind.extract_wind("b.grib2")

        for message in list(first.values()) + list(second.values()):
            self.assertEqual(message.decodes, 1)
        # The second file is on the same lattice: no rebuild.
        self.assertEqual(sum(m.lattices for m in first.values()), 1)
        self.assertEqual(sum(m.lattices for m in second.values()), 0)
        self.assertIs(again["lat"], data["lat"])
        self.assertEqual(data["lon"].dtype, np.float32)
        self.assertEqual(float(data["lon"][0, 1]), 11.0)
        self.assertTrue(np.isnan(data["speed"][1, 0]))
        self.assertTrue(data["mask"][1, 0])
        self.assertEqual(float(data["u"][1, 1]), 4.0)


class RetryWaitTests(unittest.TestCase):
    def test_backs_off_between_attempts_but_not_after_the_last(self):
        with patch.object(gribio.time, "sleep") as sleep:
            for attempt in (1, 2, 3):
                gribio.retry_wait(attempt, 3)
        self.assertEqual([call.args[0] for call in sleep.call_args_list], [3.0, 6.0])


if __name__ == "__main__":
    unittest.main()
