import contextlib
import io
import json
import os
import signal
import subprocess
import sys
import unittest
from unittest import mock

import run_go_tests


@contextlib.contextmanager
def ambient_pipes():
    owned = []
    inherited = []
    try:
        for _ in range(2):
            reader, writer = os.pipe()
            owned.extend((reader, writer))
            os.set_inheritable(reader, True)
            inherited.append(reader)
        yield inherited
    finally:
        for descriptor in owned:
            os.close(descriptor)


PROBE = """
import json, os, sys
opened = 0
for argument in sys.argv[1:]:
    try:
        os.fstat(int(argument))
        opened += 1
    except OSError:
        pass
print(json.dumps({'opened': opened}))
"""


class CleanGoHostTest(unittest.TestCase):
    @unittest.skipUnless(os.name == "posix", "native descriptor controls require POSIX")
    def test_clean_child_closes_ambient_pipes_without_changing_parent(self):
        with ambient_pipes() as descriptors:
            argv = [sys.executable, "-c", PROBE, *map(str, descriptors)]
            dirty = subprocess.run(
                argv, close_fds=False, check=True, capture_output=True, timeout=5
            )
            self.assertEqual(json.loads(dirty.stdout), {"opened": 2})
            clean = run_go_tests.run_clean(argv, capture_output=True, timeout=5)
            self.assertEqual(clean.returncode, 0)
            self.assertEqual(json.loads(clean.stdout), {"opened": 0})
            for descriptor in descriptors:
                os.fstat(descriptor)
                self.assertTrue(os.get_inheritable(descriptor))

    @unittest.skipUnless(os.name == "posix", "native descriptor controls require POSIX")
    def test_deliberate_post_boundary_descriptor_is_not_hidden(self):
        code = """
import os, subprocess, sys
reader, writer = os.pipe()
try:
    os.set_inheritable(reader, True)
    result = subprocess.run(
        [sys.executable, '-c', sys.argv[1], str(reader)],
        close_fds=False, check=True, capture_output=True, timeout=5,
    )
    sys.stdout.buffer.write(result.stdout)
finally:
    os.close(reader)
    os.close(writer)
"""
        result = run_go_tests.run_clean(
            [sys.executable, "-c", code, PROBE], capture_output=True, timeout=10
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout), {"opened": 1})

    def test_fixed_go_gate_and_stdio_environment_cwd_are_not_overridden(self):
        with mock.patch.object(run_go_tests, "run_clean") as execute:
            execute.return_value.returncode = 13
            self.assertEqual(run_go_tests.main(), 13)
            execute.assert_called_once_with(["go", "test", "./..."])

    def test_real_failure_is_not_converted_to_success(self):
        result = run_go_tests.run_clean(
            [sys.executable, "-c", "import sys; sys.exit(13)"], timeout=5
        )
        self.assertEqual(run_go_tests.exit_status(result.returncode), 13)

    def test_startup_failure_is_finite_and_nonzero(self):
        output = io.StringIO()
        with mock.patch.object(run_go_tests, "run_clean", side_effect=OSError("synthetic-private-error")):
            with contextlib.redirect_stderr(output):
                self.assertEqual(run_go_tests.main(), 127)
        self.assertEqual(output.getvalue(), "Go test host could not start\n")

    @unittest.skipUnless(os.name == "posix", "signal status requires POSIX")
    def test_real_signal_has_conventional_nonzero_status(self):
        result = run_go_tests.run_clean(
            [sys.executable, "-c", "import os, signal; os.kill(os.getpid(), signal.SIGTERM)"],
            timeout=5,
        )
        self.assertEqual(result.returncode, -signal.SIGTERM)
        self.assertEqual(run_go_tests.exit_status(result.returncode), 128 + signal.SIGTERM)


if __name__ == "__main__":
    unittest.main()
