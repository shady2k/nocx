#!/usr/bin/env python3
"""Packaging hook contract tests (not a desktop renderer substitute)."""
import importlib.util
import pathlib
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    "cwd_hook", pathlib.Path(__file__).with_name("install-cwd-hook.py")
)
hook = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hook)


class LoaderHookTest(unittest.TestCase):
    def test_updates_existing_sourced_hook_without_poisoning_loader_env(self):
        with tempfile.TemporaryDirectory() as name:
            app = pathlib.Path(name)
            (app / "apprun-hooks").mkdir()
            (app / "apprun-hooks" / hook.HOOK_NAME).write_text("# stale hook\n")
            source = f'source "$this_dir"/apprun-hooks/{hook.HOOK_NAME}\n'
            body = '#!/bin/bash\nthis_dir=/example\nsource linuxdeploy-plugin-gtk.sh\n' + source + 'exec app\n'
            (app / "AppRun").write_text(body)
            self.assertEqual(hook.install(app), 0)
            self.assertEqual((app / "AppRun").read_text(), body)
            installed = (app / "apprun-hooks" / hook.HOOK_NAME).read_text()
            self.assertIn('cd "${APPDIR:', installed)
            self.assertNotIn("export LD_LIBRARY_PATH=", installed)

    def test_inserts_hook_once_before_exec(self):
        with tempfile.TemporaryDirectory() as name:
            app = pathlib.Path(name)
            (app / "apprun-hooks").mkdir()
            (app / "AppRun").write_text('#!/bin/bash\nthis_dir=/example\nsource linuxdeploy-plugin-gtk.sh\nexec app\n')
            self.assertEqual(hook.install(app), 0)
            self.assertEqual(hook.install(app), 0)
            lines = (app / "AppRun").read_text().splitlines()
            self.assertEqual(sum(hook.HOOK_NAME in line for line in lines), 1)
            self.assertIn(hook.HOOK_NAME, lines[-2])
            self.assertEqual(lines[-1], "exec app")


if __name__ == "__main__":
    unittest.main()
