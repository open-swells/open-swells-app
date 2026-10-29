"""One file per forecast product per run, instead of one file per hour.

Layout (little-endian):

    bytes 0-3   magic b"OSB1"
    bytes 4-7   uint32 header length N
    bytes 8..   N bytes of UTF-8 JSON header
    then        gzip chunks, back to back, in header["chunks"] order

The header lists the forecast ``hours`` and the ``vars`` stored for each
hour; ``chunks`` holds the compressed byte length of every (hour, var)
chunk, hour-major, so chunk offsets are a running sum from 8 + N. One
hour's vars are contiguous, which lets the browser fetch any run of them
with a single HTTP Range request.

Two kinds of payload:

- Grids (``"kind": "grid"``): ``lat``/``lon`` are the lattice axes and each
  chunk is a row-major ``len(lat) x len(lon)`` int16 array. A value is
  ``raw * scale``; ``NODATA`` (-32768) marks land or a missing field. This
  replaces per-hour point GeoJSON, where every ocean cell repeated its
  coordinates and property names (~140 bytes vs 2 per value).
- Documents (``"kind": "geojson"``): one var, ``geojson``, and each chunk
  is that hour's GeoJSON, gzipped (the contour polygons).

Files are named per run, ``<product>_<run>.bin`` (bundle_name), and the
run's metadata.json lists them, so a URL always holds the same bytes and a
reader can keep a header as long as it likes. Each publish keeps the
previous run's bundles too (prune_bundles), for maps opened before it.

Every chunk also names itself in its gzip header's FNAME field as
``<run>/<hour>/<var>`` (see chunk_tag), which decompressors ignore: the
server's reader checks it, so a header that doesn't match its file is an
error rather than another hour's data.
"""

import gzip
import json
import os
import re
import struct

import numpy as np

MAGIC = b"OSB1"
NODATA = -32768
FORMAT_VERSION = 1
# Compression level 6 matches the per-hour .geojson.gz siblings it replaces.
COMPRESS_LEVEL = 6


def pack_grid(values: np.ndarray, scale: float, valid: np.ndarray | None = None) -> bytes:
    """Quantize one 2-D field to int16 and gzip it.

    Cells that are non-finite, or False in valid, become NODATA. Values are
    clipped to the int16 range so an outlier can't wrap around.
    """
    values = np.asarray(values, dtype=np.float64)
    ok = np.isfinite(values)
    if valid is not None:
        ok &= valid
    raw = np.full(values.shape, NODATA, dtype="<i2")
    raw[ok] = np.clip(np.round(values[ok] / scale), NODATA + 1, 32767)
    return gzip.compress(raw.tobytes(), COMPRESS_LEVEL, mtime=0)


def pack_document(payload: str) -> bytes:
    return gzip.compress(payload.encode("utf-8"), COMPRESS_LEVEL, mtime=0)


def chunk_tag(run: str | None, hour: int, name: str) -> str:
    return f"{run or ''}/{int(hour)}/{name}"


GZIP_FNAME = 0x08


def name_chunk(chunk: bytes, tag: str) -> bytes:
    """Set a plain gzip member's FNAME. The CRC covers only the data, so
    the name goes in without recompressing."""
    if chunk[:3] != b"\x1f\x8b\x08" or chunk[3] != 0:
        raise ValueError("expected a gzip member without optional header fields")
    return chunk[:3] + bytes([GZIP_FNAME]) + chunk[4:10] + tag.encode("ascii") + b"\0" + chunk[10:]


def chunk_name(chunk: bytes) -> str | None:
    """A gzip member's FNAME, or None if it has none."""
    if chunk[:3] != b"\x1f\x8b\x08" or not chunk[3] & GZIP_FNAME or chunk[3] & 0x04:
        return None  # 0x04 (FEXTRA) would come before the name; never written
    end = chunk.index(b"\0", 10)
    return chunk[10:end].decode("ascii")


def write_bundle(
    path: str,
    *,
    kind: str,
    hours: list[int],
    variables: list[dict],
    chunks: dict[int, dict[str, bytes]],
    lat: np.ndarray | None = None,
    lon: np.ndarray | None = None,
    run: str | None = None,
    axis_decimals: int = 4,
) -> int:
    """Write a bundle; returns the number of hours stored.

    variables: [{"name": ..., "scale": ...}] (scale only for grids).
    chunks: hour -> var name -> packed bytes. Hours missing from chunks, or
    missing any var, are left out of the file (and so of its ``hours``).
    Written to a temporary name and renamed so readers never see a
    half-written file.
    """
    names = [v["name"] for v in variables]
    kept = [h for h in sorted(hours) if h in chunks and all(n in chunks[h] for n in names)]
    lengths: list[int] = []
    body: list[bytes] = []
    for hour in kept:
        for name in names:
            chunk = name_chunk(chunks[hour][name], chunk_tag(run, hour, name))
            lengths.append(len(chunk))
            body.append(chunk)

    header: dict[str, object] = {
        "format": "openswells-bundle",
        "version": FORMAT_VERSION,
        "kind": kind,
        "hours": [int(h) for h in kept],
        "vars": variables,
        "chunks": lengths,
    }
    if run is not None:
        header["run"] = run
    if kind == "grid":
        header["nodata"] = NODATA
        header["lat"] = [round(float(x), axis_decimals) for x in np.asarray(lat)]
        header["lon"] = [round(float(x), axis_decimals) for x in np.asarray(lon)]
    encoded = json.dumps(header, separators=(",", ":")).encode("utf-8")

    tmp_path = path + ".part"
    with open(tmp_path, "wb") as f:
        f.write(MAGIC)
        f.write(struct.pack("<I", len(encoded)))
        f.write(encoded)
        for chunk in body:
            f.write(chunk)
    os.replace(tmp_path, path)
    return len(kept)


def bundle_name(product: str, run: str) -> str:
    """The file a run's bundle is published under, e.g. swell_20261028_12Z.bin."""
    return f"{product}_{run}.bin"


RUN_SUFFIX = re.compile(r"_(\d{8}_\d{2}Z)\.bin$")


def prune_bundles(files_dir: str, run: str) -> list[str]:
    """Delete bundles from runs other than this one and the newest before it.

    The runner leaves bundles in place between runs, so after this the
    directory holds exactly the current and previous run's; publishing
    mirrors that to the server. Returns the deleted names.
    """
    runs: dict[str, list[str]] = {}
    for name in os.listdir(files_dir):
        match = RUN_SUFFIX.search(name)
        if match:
            runs.setdefault(match.group(1), []).append(name)
    older = sorted(r for r in runs if r < run)
    keep = {run, *older[-1:]}
    deleted = []
    for other, names in runs.items():
        if other not in keep:
            for name in names:
                os.remove(os.path.join(files_dir, name))
                deleted.append(name)
    return sorted(deleted)


def read_bundle(path: str) -> tuple[dict, dict[int, dict[str, object]]]:
    """Read a bundle back: (header, hour -> var -> array or GeoJSON dict).

    Grid values come back as float arrays with NaN for NODATA. Used by the
    tests and for inspecting a run; the app has its own readers.
    """
    with open(path, "rb") as f:
        data = f.read()
    if data[:4] != MAGIC:
        raise ValueError(f"{path} is not an OpenSwells bundle")
    (header_len,) = struct.unpack("<I", data[4:8])
    header = json.loads(data[8 : 8 + header_len])
    offset = 8 + header_len
    lengths = iter(header["chunks"])
    out: dict[int, dict[str, object]] = {}
    for hour in header["hours"]:
        fields: dict[str, object] = {}
        for var in header["vars"]:
            length = next(lengths)
            chunk = data[offset : offset + length]
            expected = chunk_tag(header.get("run"), hour, var["name"])
            if chunk_name(chunk) != expected:
                raise ValueError(f"{path}: chunk {chunk_name(chunk)!r}, want {expected!r}")
            raw = gzip.decompress(chunk)
            offset += length
            if header["kind"] == "grid":
                grid = np.frombuffer(raw, dtype="<i2").reshape(
                    len(header["lat"]), len(header["lon"])
                )
                values = grid.astype(np.float64) * var["scale"]
                values[grid == header["nodata"]] = np.nan
                fields[var["name"]] = values
            else:
                fields[var["name"]] = json.loads(raw)
        out[hour] = fields
    return header, out
