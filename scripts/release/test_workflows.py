import unittest
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]


class WorkflowTriggerTest(unittest.TestCase):
    def test_ci_runs_for_mirrored_codex_review_branches(self):
        workflow = (REPOSITORY_ROOT / ".github" / "workflows" / "ci.yml").read_text()
        push_section = workflow.split("  pull_request:", 1)[0]
        self.assertIn('      - "codex/**"', push_section)
        self.assertIn("      - main", push_section)
        self.assertIn("  workflow_dispatch:", workflow)

    def test_fixture_commands_explicitly_disable_bytecode(self):
        for name in ("ci.yml", "release.yml"):
            with self.subTest(workflow=name):
                workflow = (REPOSITORY_ROOT / ".github" / "workflows" / name).read_text()
                self.assertTrue("run: python3 -B -m unittest discover -s scripts/release -p 'test_*.py'" in workflow, "release fixture command may write source-tree bytecode")


if __name__ == "__main__":
    unittest.main()
