import os
import subprocess
import tempfile
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
RUNNER = PROJECT_ROOT / "scripts" / "grib_parse_runner.sh"


class CleanupPathSafetyTests(unittest.TestCase):
    def setUp(self):
        # A throwaway OPENSWELLS_DATA_DIR, so the tests never touch the
        # machine's real /var/lib/open-swells.
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.data_dir = Path(tmp.name)
        self.pipeline_dir = self.data_dir / "pipeline"
        self.pipeline_dir.mkdir()

    def run_with_files_dir(
        self, files_dir: str, *, python_bin: str | None = None
    ) -> subprocess.CompletedProcess:
        env = os.environ.copy()
        for key in ("FILES_DIR", "LOG_DIR", "TIDE_CACHE_DIR", "FORECAST_DIR"):
            env.pop(key, None)
        with tempfile.NamedTemporaryFile() as env_file:
            env.update(
                {
                    "ENV_FILE": env_file.name,
                    "OPENSWELLS_DATA_DIR": str(self.data_dir),
                    "FILES_DIR": files_dir,
                    "PYTHON_SCRIPT": str(PROJECT_ROOT / "gfs_to_contours.py"),
                    "PYTHON_BIN": python_bin or str(PROJECT_ROOT / ".venv/bin/python"),
                }
            )
            return subprocess.run(
                ["bash", str(RUNNER)],
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )

    def assert_rejected(self, files_dir: str) -> None:
        result = self.run_with_files_dir(files_dir)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("FILES_DIR must be a child", result.stderr)

    def test_rejects_root(self):
        self.assert_rejected("/")

    def test_rejects_data_root(self):
        # It holds the app's database.
        marker = self.data_dir / "main.db"
        marker.write_text("safe")
        self.assert_rejected(str(self.data_dir))
        self.assertEqual(marker.read_text(), "safe")

    def test_rejects_published_forecast_dir(self):
        self.assert_rejected(str(self.data_dir / "forecast"))

    def test_rejects_pipeline_dir_itself(self):
        self.assert_rejected(str(self.pipeline_dir))

    def test_rejects_project_root(self):
        self.assert_rejected(str(PROJECT_ROOT))

    def test_rejects_escape_with_dotdot(self):
        self.assert_rejected(str(self.pipeline_dir / "work" / ".." / ".."))

    def test_rejects_external_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "must-survive"
            marker.write_text("safe")
            self.assert_rejected(directory)
            self.assertEqual(marker.read_text(), "safe")

    def test_rejects_child_symlink_to_external_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            link = self.pipeline_dir / "work"
            link.symlink_to(directory, target_is_directory=True)
            self.assert_rejected(str(link))

    def test_default_is_pipeline_work_dir(self):
        work = self.pipeline_dir / "work"
        work.mkdir()
        marker = work / "old-output"
        marker.write_text("delete me")
        env = os.environ.copy()
        for key in ("FILES_DIR", "LOG_DIR", "TIDE_CACHE_DIR", "FORECAST_DIR"):
            env.pop(key, None)
        with tempfile.NamedTemporaryFile() as env_file:
            env.update(
                {
                    "ENV_FILE": env_file.name,
                    "OPENSWELLS_DATA_DIR": str(self.data_dir),
                    "PYTHON_BIN": "/bin/false",
                }
            )
            result = subprocess.run(
                ["bash", str(RUNNER)], env=env, capture_output=True, text=True, check=False
            )
        self.assertNotIn("FILES_DIR must be a child", result.stderr)
        self.assertFalse(marker.exists())
        self.assertTrue((self.pipeline_dir / "logs").is_dir())

    def test_allows_and_cleans_child_directory(self):
        directory = self.pipeline_dir / "work"
        directory.mkdir()
        marker = directory / "old-output"
        marker.write_text("delete me")
        result = self.run_with_files_dir(str(directory), python_bin="/bin/false")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("FILES_DIR must be a child", result.stderr)
        self.assertFalse(marker.exists())

    def test_cleanup_keeps_run_stamped_bundles(self):
        # The pipeline prunes them itself; the previous run's must survive.
        directory = self.pipeline_dir / "work"
        directory.mkdir()
        kept = directory / "swell_20261028_06Z.bin"
        dropped = [directory / n for n in ("swell.bin", "swell_20261028_06Z.bin.part", "metadata.json")]
        for path in [kept, *dropped]:
            path.write_text("x")
        self.run_with_files_dir(str(directory), python_bin="/bin/false")
        self.assertTrue(kept.exists())
        for path in dropped:
            self.assertFalse(path.exists(), path.name)


if __name__ == "__main__":
    unittest.main()
