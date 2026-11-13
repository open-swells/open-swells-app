"""On-disk layout shared by the pipeline, the web app and dev machines.

Everything the pipeline writes lives under OPENSWELLS_DATA_DIR (default
/var/lib/open-swells), never inside the checkout; scripts/paths.sh is the
shell side of the same layout. See "Data and config layout" in the
top-level README.
"""

import os

DEFAULT_DATA_DIR = "/var/lib/open-swells"


def data_dir() -> str:
    return os.environ.get("OPENSWELLS_DATA_DIR") or DEFAULT_DATA_DIR


def pipeline_dir(*parts: str) -> str:
    """A path under the pipeline's own data directory (work/, logs/, ...)."""
    return os.path.join(data_dir(), "pipeline", *parts)


def reference_dir(*parts: str) -> str:
    """A path under reference/: downloaded inputs that are slow to fetch
    and never published (the FES2022 atlas, the GSHHG shoreline)."""
    return os.path.join(data_dir(), "reference", *parts)
