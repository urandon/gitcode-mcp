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


if __name__ == "__main__":
    unittest.main()
