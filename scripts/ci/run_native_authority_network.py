"""Run the fixed opt-in Linux authority observer gate from this checkout.

Only the test host's ambient descriptors are closed. The fixture itself passes
the real native roles and poisons inherited config/proxy inputs after that
boundary. Raw syscall input is consumed by the Go bounded streaming parser.
"""

import os
import hashlib
import sys
import tempfile

from run_go_tests import exit_status, run_clean


def main():
    try:
        with tempfile.TemporaryDirectory(prefix="native-authority-qualification-") as fixture:
            product = os.path.join(fixture, "product")
            built = run_clean(["go", "build", "-o", product, "./cmd/gitcode-mcp"])
            if built.returncode != 0:
                return exit_status(built.returncode)
            environment = dict(os.environ)
            environment["GITCODE_MCP_NATIVE_AUTHORITY_TRACE"] = "1"
            environment["GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_BINARY"] = product
            with open(product, "rb") as artifact:
                digest = hashlib.sha256()
                for chunk in iter(lambda: artifact.read(65536), b""):
                    digest.update(chunk)
                environment["GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_SHA256"] = digest.hexdigest()
            return exit_status(run_clean([
                "go", "test", "./internal/localauthority",
                "-run", "^TestNativeAuthorityStrictProcessNetworkIsolation$",
                "-count=1", "-timeout=120s",
            ], env=environment).returncode)
    except OSError:
        print("Native authority qualification host could not start", file=sys.stderr)
        return 127


if __name__ == "__main__":
    sys.exit(main())
