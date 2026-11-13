#!/usr/bin/env bash
# Fetch the FES2022b ocean_tide_extrapolated grids from AVISO+ and unpack
# them into FES_DIR (default $OPENSWELLS_DATA_DIR/reference/fes2022, i.e.
# /var/lib/open-swells/reference/fes2022), the folder the pipeline reads by
# default (see fes_tides.DEFAULT_FES_CONFIG).
#
# Needs an AVISO+ account with FES access, as a netrc file in FES_DIR:
#   machine ftp-access.aviso.altimetry.fr login <user> password <password>
# (mode 600; delete it afterwards). AVISO throttles each connection to
# ~140 KB/s, so files download six at a time. Rerunning resumes: unpacked
# .nc files are skipped and partial downloads continue.
set -uo pipefail

FES_DIR="${FES_DIR:-"${OPENSWELLS_DATA_DIR:-/var/lib/open-swells}/reference/fes2022"}"
export N="$FES_DIR/.aviso-netrc"
export B="sftp://ftp-access.aviso.altimetry.fr:2221/auxiliary/tide_model/fes2022b/ocean_tide_extrapolated"
if [ ! -f "$N" ]; then
    echo "Error: AVISO credentials not found at $N" >&2
    exit 1
fi
mkdir -p "$FES_DIR/ocean_tide_extrapolated" && cd "$FES_DIR/ocean_tide_extrapolated"

fetch() {
    f="$1"; nc="${f%.xz}"
    [ -s "$nc" ] && { echo "have $nc"; return 0; }
    for try in 1 2 3 4 5; do
        curl -sS -k --netrc-file "$N" -C - -o "$f" "$B/$f" && xz -t "$f" && break
        echo "retry $f ($try)"; sleep 20
    done
    xz -d "$f" && echo "done $nc" || echo "FAILED $f"
}
export -f fetch
curl -sS -k --netrc-file "$N" --list-only "$B/" | grep "_fes2022.nc.xz$" | sort \
    | xargs -P 6 -I{} bash -c 'fetch {}'
echo "finished: $(ls ./*_fes2022.nc 2>/dev/null | wc -l) of 34 unpacked"
