from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile
import tarfile
import unittest
import zipfile
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]


class ReleaseBuildTest(unittest.TestCase):
    def fixture(self) -> tuple[Path, dict[str, str]]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        repo = root / "source"
        (repo / "scripts/release").mkdir(parents=True)
        shutil.copy2(REPOSITORY_ROOT / "scripts/release/build.sh", repo / "scripts/release/build.sh")
        (repo / ".gitignore").write_text("dist/\n")
        (repo / "tracked.txt").write_text("fixture source\n")
        env = os.environ.copy()
        env.update({
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_SYSTEM": os.devnull, "GIT_CONFIG_COUNT": "0",
            "GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off",
        })
        for name in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS",
                     "RELEASE_COMMIT", "RELEASE_DATE", "RELEASE_VERSION", "RELEASE_TARGETS",
                     "RELEASE_FEEDBACK_REPOSITORY", "DIST_DIR", "GOOS", "GOARCH", "GOFLAGS"):
            env.pop(name, None)
        self.command(repo, env, "git", "init", "-b", "main")
        self.command(repo, env, "git", "config", "user.name", "Release Fixture")
        self.command(repo, env, "git", "config", "user.email", "release-fixture@example.invalid")
        self.command(repo, env, "git", "config", "commit.gpgsign", "false")
        return repo, env

    def command(self, repo: Path, env: dict[str, str], *args: str) -> str:
        result = subprocess.run(args, cwd=repo, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(result.returncode, 0, "isolated fixture command failed")
        return result.stdout

    def commit(self, repo: Path, env: dict[str, str]) -> str:
        self.command(repo, env, "git", "add", ".")
        self.command(repo, env, "git", "commit", "-m", "Neutral release fixture")
        return self.command(repo, env, "git", "rev-parse", "HEAD").strip()

    def test_builder_rejects_dirty_source_before_go_or_artifacts(self):
        for kind in ("tracked", "staged", "untracked"):
            with self.subTest(kind=kind):
                repo, env = self.fixture()
                self.commit(repo, env)
                if kind == "untracked":
                    (repo / "sensitive-fixture-name.txt").write_text("fixture\n")
                else:
                    (repo / "tracked.txt").write_text("changed fixture\n")
                    if kind == "staged":
                        self.command(repo, env, "git", "add", "tracked.txt")
                tools = repo.parent / "tools"
                tools.mkdir()
                marker = repo.parent / "go-called"
                fake_go = tools / "go"
                fake_go.write_text('#!/bin/sh\n: > "$GO_MARKER"\nexit 97\n')
                fake_go.chmod(0o755)
                env.update({"PATH": str(tools) + os.pathsep + env["PATH"], "GO_MARKER": str(marker)})
                result = subprocess.run(["bash", "scripts/release/build.sh"], cwd=repo, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                self.assertNotEqual(result.returncode, 0, "dirty source was accepted")
                self.assertFalse(marker.exists(), "builder invoked Go before rejecting source changes")
                self.assertFalse((repo / "dist").exists(), "builder created artifacts before rejecting source changes")
                self.assertTrue("source tree is not clean" in result.stderr, "missing finite cleanliness diagnostic")
                self.assertFalse("tracked.txt" in result.stderr or "sensitive-fixture-name" in result.stderr or str(repo) in result.stderr, "cleanliness diagnostic exposed source coordinates")

    def test_fixture_command_disables_bytecode_with_neutral_python_defaults(self):
        repo, env = self.fixture()
        # Run the real release fixture suite, excluding only this recursive
        # harness. No host cache-prefix setting may conceal source pollution.
        for source in (REPOSITORY_ROOT / "scripts/release").glob("*.py"):
            if source.name != Path(__file__).name:
                shutil.copy2(source, repo / "scripts/release" / source.name)
        for name in ("ci.yml", "release.yml"):
            destination = repo / ".github/workflows" / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPOSITORY_ROOT / ".github/workflows" / name, destination)
        self.commit(repo, env)
        code = (
            "import sys,runpy; sys.pycache_prefix=None; "
            "assert sys.dont_write_bytecode; "
            "sys.argv=['unittest','discover','-s','scripts/release','-p','test_*.py']; "
            "runpy.run_module('unittest',run_name='__main__',alter_sys=True)"
        )
        self.command(repo, env, sys.executable, "-B", "-c", code)
        status = self.command(repo, env, "git", "status", "--porcelain=v1", "--untracked-files=all")
        self.assertFalse(status, "release fixtures polluted neutral source tree")
        self.assertFalse(list(repo.rglob("__pycache__")), "fixture runner created source bytecode")

    def test_builder_rechecks_source_around_each_go_build(self):
        for kind in ("unignored-artifacts", "source-mutated-during-go"):
            with self.subTest(kind=kind):
                repo, env = self.fixture()
                self.commit(repo, env)
                tools = repo.parent / "tools"
                tools.mkdir()
                fake_go = tools / "go"
                marker = repo.parent / "go-called"
                fake_go.write_text('#!/bin/sh\n: > "$GO_MARKER"\nprintf changed > tracked.txt\n')
                fake_go.chmod(0o755)
                env.update({"PATH": str(tools) + os.pathsep + env["PATH"], "GO_MARKER": str(marker)})
                if kind == "unignored-artifacts":
                    env["DIST_DIR"] = str(repo / "unignored-artifacts")
                result = subprocess.run(["bash", "scripts/release/build.sh"], cwd=repo, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                self.assertNotEqual(result.returncode, 0, "source became dirty without rejecting packaging")
                self.assertTrue("source tree is not clean" in result.stderr, "missing finite cleanliness diagnostic")
                self.assertTrue(marker.exists() == (kind == "source-mutated-during-go"), "cleanliness guard ran on the wrong side of Go")
                self.assertFalse(list(repo.rglob("*.tar.gz")) or list(repo.rglob("*.zip")), "dirty build was packaged")

    def test_clean_builder_preserves_version_revision_and_clean_vcs_stamp(self):
        repo, env = self.fixture()
        (repo / "go.mod").write_text("module gitcode-mcp\n\ngo 1.22\n")
        package = repo / "internal/buildinfo"
        package.mkdir(parents=True)
        (package / "buildinfo.go").write_text("package buildinfo\nvar Version, Commit, Date, FeedbackRepository string\n")
        main = repo / "cmd/gitcode-mcp"
        main.mkdir(parents=True)
        (main / "main.go").write_text('package main\nimport("fmt"; "os"; "gitcode-mcp/internal/buildinfo")\nfunc main(){if len(os.Args)>1 && os.Args[1]=="commit" {fmt.Println(buildinfo.Commit);return}; fmt.Println("gitcode-mcp " + buildinfo.Version)}\n')
        revision = self.commit(repo, env)
        host = self.command(repo, env, "go", "env", "GOHOSTOS", "GOHOSTARCH").splitlines()
        self.assertEqual(len(host), 2, "missing Go host target")
        env.update({"RELEASE_VERSION": "v9.8.7", "RELEASE_TARGETS": "/".join(host)})
        self.command(repo, env, "bash", "scripts/release/build.sh")
        binary = repo / "dist" / ("gitcode-mcp_v9.8.7_" + "_".join(host)) / ("gitcode-mcp.exe" if host[0] == "windows" else "gitcode-mcp")
        metadata = self.command(repo, env, "go", "version", "-m", str(binary))
        for expected in ("vcs.revision=" + revision, "vcs.modified=false"):
            self.assertTrue(expected in metadata, "release binary lost expected clean build provenance")
        version = self.command(repo, env, str(binary), "--version").strip()
        self.assertTrue(version == "gitcode-mcp 9.8.7", "release binary version mismatch")
        self.assertTrue(self.command(repo, env, str(binary), "commit").strip() == revision, "release binary commit mismatch")
        self.assertFalse(self.command(repo, env, "git", "status", "--porcelain=v1", "--untracked-files=all"), "clean builder polluted source tree")

    def archive_fixture(self, *, native_attributes: bool = False):
        repo, env = self.fixture()
        self.commit(repo, env)
        tools = repo.parent / "tools"
        tools.mkdir()
        fake_go = tools / "go"
        fake_go.write_text(
            '#!/bin/sh\nset -eu\n'
            'while [ "$#" -gt 0 ]; do\n'
            '  if [ "$1" = "-o" ]; then shift; output="$1"; break; fi\n'
            '  shift\n'
            'done\n'
            'printf "fixture executable\\n" > "$output"\nchmod 755 "$output"\n'
            'if [ "${SYNTHETIC_XATTR:-}" = "1" ]; then\n'
            '  xattr -w com.example.release-fixture fixture "$output"\nfi\n'
        )
        fake_go.chmod(0o755)
        env.update({"PATH": str(tools) + os.pathsep + env["PATH"],
                    "RELEASE_VERSION": "v9.8.7", "RELEASE_TARGETS": "linux/amd64 windows/amd64",
                    "SYNTHETIC_XATTR": "1" if native_attributes else "0"})
        return repo, env, tools

    def assert_archive_contract(self, repo: Path):
        dist = repo / "dist"
        for target, extension, binary in (("linux_amd64", ".tar.gz", "gitcode-mcp"),
                                          ("windows_amd64", ".zip", "gitcode-mcp.exe")):
            package = "gitcode-mcp_v9.8.7_" + target
            archive = dist / (package + extension)
            expected = {package, package + "/README.txt", package + "/" + binary}
            if extension == ".tar.gz":
                with tarfile.open(archive) as stream:
                    members = stream.getmembers()
                    self.assertTrue(len(members) == 3 and {m.name.rstrip("/") for m in members} == expected,
                                    "Unix archive contains unexpected members")
                    self.assertEqual(stream.getmember(package + "/" + binary).mode & 0o777, 0o755,
                                     "Unix archive lost executable mode")
                    files = {name: stream.extractfile(package + "/" + name).read() for name in ("README.txt", binary)}
            else:
                with zipfile.ZipFile(archive) as stream:
                    self.assertTrue(len(stream.infolist()) == 3 and {n.rstrip("/") for n in stream.namelist()} == expected,
                                    "Windows archive contains unexpected members")
                    files = {name: stream.read(package + "/" + name) for name in ("README.txt", binary)}
            self.assertEqual(files[binary], b"fixture executable\n", "archive changed executable bytes")
            self.assertEqual(files["README.txt"], (dist / package / "README.txt").read_bytes(), "archive changed README bytes")
            checksum = hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name
            self.assertIn(checksum, (dist / "checksums.txt").read_text().splitlines(), "archive checksum mismatch")

    def test_builder_explicitly_suppresses_tar_metadata(self):
        for caller_value in (None, "0"):
            with self.subTest(caller_value=caller_value):
                repo, env, tools = self.archive_fixture()
                env["REAL_TAR"] = shutil.which("tar")
                wrapper = tools / "tar"
                wrapper.write_text('#!/bin/sh\n[ "${COPYFILE_DISABLE:-}" = "1" ] || exit 98\nexec "$REAL_TAR" "$@"\n')
                wrapper.chmod(0o755)
                env.pop("COPYFILE_DISABLE", None)
                if caller_value is not None:
                    env["COPYFILE_DISABLE"] = caller_value
                self.command(repo, env, "bash", "scripts/release/build.sh")
                self.assert_archive_contract(repo)

    @unittest.skipUnless(sys.platform == "darwin", "native macOS copyfile qualification")
    def test_native_macos_archives_exclude_synthetic_extended_attributes(self):
        repo, env, _ = self.archive_fixture(native_attributes=True)
        env.pop("COPYFILE_DISABLE", None)
        self.command(repo, env, "bash", "scripts/release/build.sh")
        self.assert_archive_contract(repo)


if __name__ == "__main__":
    unittest.main()
