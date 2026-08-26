from __future__ import annotations

import hashlib
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

import generate_notes as notes  # noqa: E402


def run_git(repo: Path, *args: str) -> str:
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    ).stdout


class ReleaseNotesTest(unittest.TestCase):
    def create_repo(self) -> Path:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        repo = Path(temporary.name)
        run_git(repo, "init", "-b", "main")
        run_git(repo, "config", "user.email", "release-test@example.invalid")
        run_git(repo, "config", "user.name", "Release Test")
        return repo

    def commit(self, repo: Path, subject: str, body: str = "") -> str:
        arguments = ["commit", "--allow-empty", "-m", subject]
        if body:
            arguments.extend(["-m", body])
        run_git(repo, *arguments)
        return run_git(repo, "rev-parse", "HEAD").strip()

    def test_real_git_history_groups_gitcode_merge_metadata(self) -> None:
        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        self.commit(
            repo,
            "!12 merge topic into main",
            "\n".join(
                (
                    "Support inline replies",
                    "",
                    "Created-by: example",
                    "Description: Closes #7, #8 and #9",
                    "Labels: mcp, enhancement",
                    "BREAKING CHANGE: update the client configuration",
                )
            ),
        )
        direct_sha = self.commit(repo, "Stabilize parser errors")
        run_git(repo, "tag", "v0.2.0")

        result = notes.generate(
            git=notes.GitRepository(repo),
            tag="v0.2.0",
            previous_tag="",
            preview=False,
            web_base_url="https://gitcode.com/example/project",
            supplement="Maintainer-authored context.",
            assets=(
                notes.Asset("z.zip", "https://example.invalid/z.zip"),
                notes.Asset("a.tar.gz", "https://example.invalid/a.tar.gz"),
            ),
            highlight_count=3,
        )

        self.assertEqual(result.previous_tag, "v0.1.0")
        self.assertEqual(len(result.entries), 2)
        direct_entry, merge_entry = result.entries
        self.assertEqual(merge_entry.title, "Support inline replies")
        self.assertEqual(merge_entry.merge_request, 12)
        self.assertEqual(merge_entry.issues, (7, 8, 9))
        self.assertEqual(merge_entry.category, "added")
        self.assertTrue(merge_entry.breaking)
        self.assertEqual(direct_entry.category, "fixed")
        self.assertIn(
            "[!12](https://gitcode.com/example/project/merge_requests/12)",
            result.content,
        )
        self.assertIn(
            "[#9](https://gitcode.com/example/project/issues/9)",
            result.content,
        )
        self.assertIn(
            f"[{direct_sha[:8]}](https://gitcode.com/example/project/commit/{direct_sha})",
            result.content,
        )
        self.assertIn("## Maintainer notes", result.content)
        self.assertIn("Maintainer-authored context.", result.content)
        self.assertLess(result.content.index("a.tar.gz"), result.content.index("z.zip"))
        self.assertEqual(
            result.fingerprint,
            hashlib.sha256(result.content.encode("utf-8")).hexdigest(),
        )

        repeated = notes.generate(
            git=notes.GitRepository(repo),
            tag="v0.2.0",
            previous_tag="",
            preview=False,
            web_base_url="https://gitcode.com/example/project",
            supplement="Maintainer-authored context.",
            assets=(
                notes.Asset("z.zip", "https://example.invalid/z.zip"),
                notes.Asset("a.tar.gz", "https://example.invalid/a.tar.gz"),
            ),
            highlight_count=3,
        )
        self.assertEqual(repeated.content, result.content)
        self.assertEqual(repeated.fingerprint, result.fingerprint)

    def test_migration_word_is_not_implicitly_breaking(self) -> None:
        raw = (
            "a" * 40
            + "\x1f!2 merge schema into main"
            + "\x1fAdd cache migration coverage\n\n"
            + "Description: Fixes #1\n"
            + "Verification includes schema migration tests.\x1e"
        )
        entries = notes.parse_entries(raw)
        self.assertEqual(len(entries), 1)
        self.assertFalse(entries[0].breaking)

    def test_preview_uses_head_and_latest_first_parent_tag(self) -> None:
        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        self.commit(repo, "Add planned feature")

        result = notes.generate(
            git=notes.GitRepository(repo),
            tag="v0.2.0",
            previous_tag="",
            preview=True,
            web_base_url="https://gitcode.com/example/project",
            supplement="",
            assets=(),
            highlight_count=1,
        )

        self.assertTrue(result.preview)
        self.assertEqual(result.target, "HEAD")
        self.assertEqual(result.previous_tag, "v0.1.0")
        self.assertIn("pre-tag preview generated from `HEAD`", result.content)

    def test_explicit_previous_tag_must_be_on_first_parent_history(self) -> None:
        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        run_git(repo, "checkout", "-b", "side")
        self.commit(repo, "Side release")
        run_git(repo, "tag", "v9.9.9")
        run_git(repo, "checkout", "main")
        self.commit(repo, "Main release")
        run_git(repo, "tag", "v0.2.0")

        with self.assertRaisesRegex(
            notes.ReleaseNotesError, "not on the first-parent history"
        ):
            notes.generate(
                git=notes.GitRepository(repo),
                tag="v0.2.0",
                previous_tag="v9.9.9",
                preview=False,
                web_base_url="https://gitcode.com/example/project",
                supplement="",
                assets=(),
                highlight_count=1,
            )

    def test_automatic_previous_tag_ignores_tagged_merged_branch(self) -> None:
        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        run_git(repo, "checkout", "-b", "side")
        self.commit(repo, "Side branch work")
        run_git(repo, "tag", "v9.9.9")
        run_git(repo, "checkout", "main")
        run_git(
            repo,
            "merge",
            "--no-ff",
            "side",
            "-m",
            "!2 merge side into main",
            "-m",
            "Merge side branch",
        )
        run_git(repo, "tag", "v0.2.0")

        result = notes.generate(
            git=notes.GitRepository(repo),
            tag="v0.2.0",
            previous_tag="",
            preview=False,
            web_base_url="https://gitcode.com/example/project",
            supplement="",
            assets=(),
            highlight_count=1,
        )

        self.assertEqual(result.previous_tag, "v0.1.0")

    def test_invalid_tag_asset_and_empty_range_fail(self) -> None:
        with self.assertRaisesRegex(notes.ReleaseNotesError, "v-prefixed SemVer"):
            notes.validate_tag("release-1")
        with self.assertRaisesRegex(notes.ReleaseNotesError, "absolute HTTPS"):
            notes.parse_asset("binary=http://example.invalid/binary")

        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        run_git(repo, "tag", "v0.1.1")
        with self.assertRaisesRegex(notes.ReleaseNotesError, "no delivered changes"):
            notes.generate(
                git=notes.GitRepository(repo),
                tag="v0.1.1",
                previous_tag="v0.1.0",
                preview=False,
                web_base_url="https://gitcode.com/example/project",
                supplement="",
                assets=(),
                highlight_count=1,
            )

    def test_cli_writes_output_and_reports_fingerprint(self) -> None:
        repo = self.create_repo()
        self.commit(repo, "Initial release")
        run_git(repo, "tag", "v0.1.0")
        self.commit(repo, "Add release automation")
        output = repo / "dist" / "release-notes.md"

        completed = subprocess.run(
            [
                sys.executable,
                str(SCRIPT_DIR / "generate_notes.py"),
                "--repo-dir",
                str(repo),
                "--tag",
                "v0.2.0",
                "--preview",
                "--previous-tag",
                "v0.1.0",
                "--gitcode-web-base-url",
                "https://gitcode.com/example/project",
                "--asset",
                "binary.tar.gz=https://example.invalid/binary.tar.gz",
                "--output",
                str(output),
            ],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertTrue(output.is_file())
        self.assertIn("fingerprint=", completed.stdout)
        self.assertIn("## Assets", output.read_text(encoding="utf-8"))

    def test_release_workflow_reuses_one_notes_file(self) -> None:
        repository_root = SCRIPT_DIR.parents[1]
        workflow = (
            repository_root / ".github" / "workflows" / "release.yml"
        ).read_text(encoding="utf-8")

        self.assertIn('notes_path="dist/release-notes.md"', workflow)
        self.assertIn("--notes-file dist/release-notes.md", workflow)
        self.assertIn('--input "$notes_path"', workflow)
        self.assertLess(
            workflow.index("Wait for GitCode tag mirror"),
            workflow.index("Publish GitCode release"),
        )
        self.assertIn("scripts/release/wait_gitcode_tag.py", workflow)
        self.assertIn('--expected-commit "$(git rev-parse HEAD)"', workflow)
        self.assertNotIn(
            "release artifacts and SHA256 checksums.", workflow
        )


if __name__ == "__main__":
    unittest.main()
