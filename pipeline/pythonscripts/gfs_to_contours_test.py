import os

import requests

from gfs_to_contours import (
    logger,
    setup_logging,
    find_latest_gfs_time,
    write_metadata,
    process_forecast_hours,
)


def main() -> None:
    files_dir = os.environ.get("FILES_DIR")
    if not files_dir:
        raise EnvironmentError("FILES_DIR environment variable is not set")
    log_dir = os.environ.get("LOG_DIR")
    if not log_dir:
        raise EnvironmentError("LOG_DIR environment variable is not set")

    setup_logging(log_dir)

    stride = max(int(os.environ.get("CONTOUR_STRIDE", "2") or 1), 1)
    smoothing_sigma = float(os.environ.get("CONTOUR_SMOOTHING_SIGMA", "1.0") or 1.0)
    simplify_env = os.environ.get("CONTOUR_SIMPLIFY_TOLERANCE")
    simplify_tolerance = float(simplify_env) if simplify_env else None

    with requests.Session() as session:
        date_str, hour = find_latest_gfs_time(session=session)
        logger.info(
            "[test] Found latest GFS wave data for date %s hour %sZ", date_str, hour
        )
        write_metadata(files_dir, date_str, hour)
        process_forecast_hours(
            range(0, 2),
            date_str,
            hour,
            files_dir,
            stride=stride,
            smoothing_sigma=smoothing_sigma,
            simplify_tolerance=simplify_tolerance,
            workers=1,
        )


if __name__ == "__main__":
    main()
