import contextlib
import io
import os
import unittest
from unittest import mock

import run_native_authority_network as gate


class NativeAuthorityNetworkGateTest(unittest.TestCase):
    def test_fixed_current_checkout_build_and_observation(self):
        with mock.patch.object(gate, "run_clean") as execute:
            def build_and_observe(argv, **options):
                if argv[1] == "build":
                    with open(argv[3], "wb") as artifact:
                        artifact.write(b"synthetic-product")
                return mock.Mock(returncode=0)
            execute.side_effect = build_and_observe
            self.assertEqual(gate.main(), 0)
            build, observe = execute.call_args_list
            binary = build.args[0][3]
            self.assertEqual(build.args[0], ["go", "build", "-o", binary, "./cmd/gitcode-mcp"])
            self.assertEqual(build.kwargs, {})
            self.assertEqual(observe.args[0], [
                "go", "test", "./internal/localauthority",
                "-run", "^TestNativeAuthorityStrictProcessNetworkIsolation$",
                "-count=1", "-timeout=120s",
            ])
            self.assertEqual(observe.kwargs["env"]["GITCODE_MCP_NATIVE_AUTHORITY_TRACE"], "1")
            self.assertEqual(observe.kwargs["env"]["GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_BINARY"], binary)
            self.assertEqual(len(observe.kwargs["env"]["GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_SHA256"]), 64)
            self.assertFalse(os.path.exists(os.path.dirname(binary)))

    def test_failed_build_never_runs_observer(self):
        with mock.patch.object(gate, "run_clean") as execute:
            execute.return_value.returncode = 13
            self.assertEqual(gate.main(), 13)
            self.assertEqual(execute.call_count, 1)

    def test_failed_or_signalled_observer_is_not_green(self):
        for code, expected in [(13, 13), (-15, 143)]:
            with self.subTest(code=code), mock.patch.object(gate, "run_clean") as execute:
                def build_and_observe(argv, **options):
                    if argv[1] == "build":
                        with open(argv[3], "wb") as artifact:
                            artifact.write(b"synthetic-product")
                        return mock.Mock(returncode=0)
                    return mock.Mock(returncode=code)
                execute.side_effect = build_and_observe
                self.assertEqual(gate.main(), expected)

    def test_missing_tool_is_sanitized_and_nonzero(self):
        output = io.StringIO()
        with mock.patch.object(gate, "run_clean", side_effect=OSError("synthetic-private-path")):
            with contextlib.redirect_stderr(output):
                self.assertEqual(gate.main(), 127)
        self.assertEqual(output.getvalue(), "Native authority qualification host could not start\n")


if __name__ == "__main__":
    unittest.main()
