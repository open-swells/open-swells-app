import os
import subprocess
import tempfile
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
RUNNER = PROJECT_ROOT / "scripts" / "grib_parse_runner.sh"


class CleanupPathSafetyTests(unittest.TestCase):
    def run_with_files_dir(
        self, files_dir: str, *, python_bin: str | None = None
    ) -> subprocess.CompletedProcess:
        env = os.environ.copy()
        with tempfile.NamedTemporaryFile() as env_file:
            env.update(
                {
                    "ENV_FILE": env_file.name,
                    "FILES_DIR": files_dir,
                    "LOG_DIR": str(PROJECT_ROOT / "logs"),
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

    def test_rejects_project_root(self):
        self.assert_rejected(str(PROJECT_ROOT))

    def test_rejects_external_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "must-survive"
            marker.write_text("safe")
            self.assert_rejected(directory)
            self.assertEqual(marker.read_text(), "safe")

    def test_rejects_child_symlink_to_external_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            link = PROJECT_ROOT / ".cleanup-safety-test-link"
            try:
                link.symlink_to(directory, target_is_directory=True)
                self.assert_rejected(str(link))
            finally:
                link.unlink(missing_ok=True)

    def test_allows_and_cleans_child_directory(self):
        with tempfile.TemporaryDirectory(dir=PROJECT_ROOT) as directory:
            marker = Path(directory) / "old-output"
            marker.write_text("delete me")
            result = self.run_with_files_dir(directory, python_bin="/bin/false")
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("FILES_DIR must be a child", result.stderr)
            self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
