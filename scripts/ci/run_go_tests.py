"""Run the fixed offline Go gate without ambient runner descriptors.

This is test-host isolation, not product startup qualification. Native tests
still create their intentional inherited-role controls after this boundary.
"""

import subprocess
import sys


def run_clean(argv, **options):
    return subprocess.run(argv, shell=False, close_fds=True, check=False, **options)


def exit_status(returncode):
    return returncode if returncode >= 0 else 128 - returncode


def main():
    try:
        return exit_status(run_clean(["go", "test", "./..."]).returncode)
    except OSError:
        print("Go test host could not start", file=sys.stderr)
        return 127


if __name__ == "__main__":
    sys.exit(main())
