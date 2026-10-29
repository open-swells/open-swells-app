import gzip
import json
import os
import struct
import tempfile
import unittest

import numpy as np

import bundle
import gfs_to_contours
import wind


class BundleTests(unittest.TestCase):
    def test_grid_round_trip_quantizes_and_marks_nodata(self):
        values = np.array([[1.234, np.nan], [-0.004, 400.0]])
        valid = np.array([[True, True], [True, False]])
        chunks = {3: {"h": bundle.pack_grid(values, 0.01, valid)}}
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "x.bin")
            count = bundle.write_bundle(
                path, kind="grid", hours=[3], variables=[{"name": "h", "scale": 0.01}],
                chunks=chunks, lat=np.array([1.0, 0.0]), lon=np.array([5.0, 6.0]),
            )
            header, frames = bundle.read_bundle(path)
            self.assertFalse(os.path.exists(path + ".part"))
        self.assertEqual(count, 1)
        self.assertEqual(header["nodata"], bundle.NODATA)
        grid = frames[3]["h"]
        self.assertAlmostEqual(grid[0, 0], 1.23)
        self.assertTrue(np.isnan(grid[0, 1]))  # non-finite
        self.assertAlmostEqual(grid[1, 0], 0.0)
        self.assertTrue(np.isnan(grid[1, 1]))  # invalid

    def test_layout_is_magic_length_header_then_contiguous_named_chunks(self):
        # The Go and JS readers depend on this exact layout.
        payloads = {(0, "a"): b"first", (0, "b"): b"second", (1, "a"): b"third", (1, "b"): b"fourth!"}
        chunks = {hour: {} for hour in (0, 1, 2)}
        for (hour, name), payload in payloads.items():
            chunks[hour][name] = bundle.pack_document(payload.decode())
        chunks[2]["a"] = bundle.pack_document("no b")  # incomplete hours are left out
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "x.bin")
            bundle.write_bundle(
                path, kind="geojson", hours=[1, 0, 2],
                variables=[{"name": "a"}, {"name": "b"}], chunks=chunks, run="20261028_12Z",
            )
            with open(path, "rb") as f:
                data = f.read()
        self.assertEqual(data[:4], b"OSB1")
        (length,) = struct.unpack("<I", data[4:8])
        header = json.loads(data[8 : 8 + length])
        self.assertEqual(header["hours"], [0, 1])
        offset = 8 + length
        for (hour, name), size in zip([(0, "a"), (0, "b"), (1, "a"), (1, "b")], header["chunks"]):
            chunk = data[offset : offset + size]
            # Each chunk names itself; decompressors ignore the name.
            self.assertEqual(bundle.chunk_name(chunk), f"20261028_12Z/{hour}/{name}")
            self.assertEqual(gzip.decompress(chunk), payloads[(hour, name)])
            offset += size
        self.assertEqual(offset, len(data))

    def test_reading_a_chunk_under_the_wrong_name_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "x.bin")
            bundle.write_bundle(
                path, kind="geojson", hours=[0, 3], variables=[{"name": "geojson"}],
                chunks={0: {"geojson": bundle.pack_document("{}")}, 3: {"geojson": bundle.pack_document("[]")}},
                run="r",
            )
            with open(path, "rb") as f:
                data = bytearray(f.read())
            (length,) = struct.unpack("<I", data[4:8])
            header = json.loads(data[8 : 8 + length])
            header["hours"] = [3, 0]  # a header whose offsets point at the wrong hours
            encoded = json.dumps(header).encode()
            data[4 : 8 + length] = struct.pack("<I", len(encoded)) + encoded
            with open(path, "wb") as f:
                f.write(data)
            with self.assertRaisesRegex(ValueError, "want 'r/3/geojson'"):
                bundle.read_bundle(path)

    def test_prune_keeps_this_run_and_the_one_before(self):
        with tempfile.TemporaryDirectory() as tmp:
            names = [
                "swell_20261027_18Z.bin", "wind_20261027_18Z.bin",
                "swell_20261028_00Z.bin", "nwps_points_lox_cg1_20261028_00Z.bin",
                "swell_20261028_06Z.bin", "wind_20261028_06Z.bin",
                "swell_20261028_12Z.bin",  # newer, but not the run being published
                "heatmap_000.png", "swell.bin.part",
            ]
            for name in names:
                open(os.path.join(tmp, name), "w").close()
            deleted = bundle.prune_bundles(tmp, "20261028_06Z")
            self.assertEqual(deleted, ["swell_20261027_18Z.bin", "swell_20261028_12Z.bin", "wind_20261027_18Z.bin"])
            self.assertEqual(sorted(os.listdir(tmp)), sorted(set(names) - set(deleted)))
        self.assertEqual(bundle.bundle_name("nwps_points_lox_cg1", "20261028_06Z"),
                         "nwps_points_lox_cg1_20261028_06Z.bin")

    def test_values_clip_instead_of_wrapping(self):
        raw = gzip.decompress(bundle.pack_grid(np.array([[1e9, -1e9]]), 0.01))
        self.assertEqual(list(np.frombuffer(raw, "<i2")), [32767, -32767])

    def test_swell_and_wind_packing(self):
        shape = (20, 20)
        partitions = []
        for sequence in (1, 2, 3):
            height = np.full(shape, float(sequence), np.float32)
            if sequence == 2:
                height[0, 0] = np.nan  # partition 2 absent at the first cell
            partitions.append({
                "sequence": sequence,
                "height": height,
                "period": np.full(shape, 12.34, np.float32),
                "direction": np.full(shape, 359.7, np.float32),
                "mask": np.zeros(shape, bool),
            })
        swell = gfs_to_contours.pack_swell_partitions({"swell_partitions": partitions}, stride=10)
        self.assertEqual(set(swell), {v["name"] for v in gfs_to_contours.SWELL_VARS})
        p2 = np.frombuffer(gzip.decompress(swell["p2"]), "<i2").reshape(2, 2)
        self.assertEqual(p2[0, 0], bundle.NODATA)  # the whole system is absent
        self.assertEqual(p2[1, 1], 123)
        d1 = np.frombuffer(gzip.decompress(swell["d1"]), "<i2")
        self.assertTrue(np.all(d1 == 0))  # 360 wraps to 0

        speeds = np.full(shape, 5.55, np.float32)
        speeds[10, 10] = np.nan
        packed = wind.pack_wind({
            "speed": speeds,
            "direction": np.full(shape, 90.0, np.float32),
            "u": np.full(shape, -5.5, np.float32),
            "v": np.zeros(shape, np.float32),
            "mask": np.zeros(shape, bool),
        }, stride=10)
        u = np.frombuffer(gzip.decompress(packed["u"]), "<i2").reshape(2, 2)
        self.assertEqual(u[0, 0], -55)
        self.assertEqual(u[1, 1], bundle.NODATA)  # any invalid var drops the cell


if __name__ == "__main__":
    unittest.main()
