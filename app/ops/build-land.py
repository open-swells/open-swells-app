#!/usr/bin/env python3
"""Build app/data/land_10m.bin.gz, the coastline the share cards draw.

The source is Natural Earth's 1:10m land polygons (public domain):

    curl -sSLO https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_land.geojson
    python3 app/ops/build-land.py ne_10m_land.geojson app/data/land_10m.bin.gz

Format (gzip of): the magic b"OSLAND1\\n", then a uvarint ring count, then
per ring a uvarint point count and zigzag-varint deltas of lon, lat in
1e-4 degree units (about 11 m). Exterior rings are written counterclockwise
and holes clockwise, so a nonzero-winding fill cuts the holes out. The
reader is app/server/land.go.
"""

import gzip
import json
import sys

SCALE = 1e4


def signed_area(ring):
    return sum(x0 * y1 - x1 * y0 for (x0, y0), (x1, y1) in zip(ring, ring[1:] + ring[:1])) / 2


def uvarint(out, value):
    while value >= 0x80:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value)


def zigzag(value):
    return (value << 1) ^ (value >> 63)


def main(src, dst):
    with open(src) as f:
        features = json.load(f)["features"]
    rings = []
    for feature in features:
        geometry = feature["geometry"]
        polygons = [geometry["coordinates"]] if geometry["type"] == "Polygon" else geometry["coordinates"]
        for polygon in polygons:
            for index, ring in enumerate(polygon):
                points = [(round(lon * SCALE), round(lat * SCALE)) for lon, lat in ring]
                if len(points) > 1 and points[0] == points[-1]:
                    points.pop()
                if len(points) < 3:
                    continue
                exterior = index == 0
                if (signed_area(points) > 0) != exterior:
                    points.reverse()
                rings.append(points)

    out = bytearray(b"OSLAND1\n")
    uvarint(out, len(rings))
    for ring in rings:
        uvarint(out, len(ring))
        px = py = 0
        for x, y in ring:
            uvarint(out, zigzag(x - px))
            uvarint(out, zigzag(y - py))
            px, py = x, y
    with open(dst, "wb") as f:
        f.write(gzip.compress(bytes(out), compresslevel=9, mtime=0))
    print(f"{len(rings)} rings, {sum(map(len, rings))} points -> {dst}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
