import importlib.util
import subprocess
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("wait_gitcode_tag.py")
SPEC = importlib.util.spec_from_file_location("wait_gitcode_tag", MODULE_PATH)
wait_tag = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(wait_tag)


class Clock:
    def __init__(self):
        self.value = 0.0

    def monotonic(self):
        return self.value

    def sleep(self, seconds):
        self.value += seconds


class WaitGitCodeTagTest(unittest.TestCase):
    commit = "8e94ad54bfdfeaf6fdd1e93cca385a841acbf039"

    def result(self, code=0, output=""):
        return subprocess.CompletedProcess([], code, stdout=output, stderr="")

    def test_waits_for_annotated_tag_peeled_commit(self):
        clock = Clock()
        calls = iter([
            self.result(2),
            self.result(0, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\trefs/tags/v0.2.1\n" + self.commit + "\trefs/tags/v0.2.1^{}\n"),
        ])
        attempts = wait_tag.wait_for_tag(
            repo_url="https://gitcode.com/owner/repo.git",
            tag="v0.2.1",
            expected_commit=self.commit,
            timeout=30,
            interval=5,
            run=lambda *args, **kwargs: next(calls),
            monotonic=clock.monotonic,
            sleep=clock.sleep,
        )
        self.assertEqual(attempts, 2)
        self.assertEqual(clock.value, 5)

    def test_rejects_wrong_existing_tag(self):
        wrong = "1" * 40
        with self.assertRaisesRegex(wait_tag.TagWaitError, "does not resolve"):
            wait_tag.wait_for_tag(
                repo_url="https://gitcode.com/owner/repo.git",
                tag="v0.2.1",
                expected_commit=self.commit,
                run=lambda *args, **kwargs: self.result(0, wrong + "\trefs/tags/v0.2.1\n"),
            )

    def test_timeout_is_bounded_and_public_safe(self):
        clock = Clock()
        with self.assertRaisesRegex(wait_tag.TagWaitError, "bounded timeout"):
            wait_tag.wait_for_tag(
                repo_url="https://gitcode.com/owner/repo.git",
                tag="v0.2.1",
                expected_commit=self.commit,
                timeout=10,
                interval=5,
                run=lambda *args, **kwargs: self.result(2),
                monotonic=clock.monotonic,
                sleep=clock.sleep,
            )
        self.assertEqual(clock.value, 10)

    def test_rejects_unbounded_or_unsafe_inputs(self):
        cases = [
            {"repo_url": "http://gitcode.example/repo.git", "tag": "v1", "expected_commit": self.commit},
            {"repo_url": "https://gitcode.example/repo.git", "tag": "release-1", "expected_commit": self.commit},
            {"repo_url": "https://gitcode.example/repo.git", "tag": "v1", "expected_commit": "short"},
        ]
        for case in cases:
            with self.subTest(case=case), self.assertRaises(wait_tag.TagWaitError):
                wait_tag.wait_for_tag(**case)


if __name__ == "__main__":
    unittest.main()
