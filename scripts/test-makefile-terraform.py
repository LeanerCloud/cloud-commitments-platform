#!/usr/bin/env python3
"""Gate for Makefile.terraform validate/state-* targets and dependabot.

The dry-run output of each target must reference only terraform/environments
paths that exist (the flat per-provider root), never the removed per-profile
layout. Backend and var files are gitignored and absent in a clean checkout,
so only directories and the flat root are checked for existence."""
import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PATH_RE = re.compile(r"terraform/environments/[A-Za-z0-9_./$()-]+")


def dry_run(target, *extra):
    out = subprocess.run(
        ["make", "-n", "-f", "Makefile.terraform", target, "PROVIDER=aws", "PROFILE=dev", *extra],
        cwd=ROOT, capture_output=True, text=True, check=True,
    ).stdout
    return out


class MakefileTerraformTest(unittest.TestCase):
    def check(self, target, *extra):
        out = dry_run(target, *extra)
        paths = PATH_RE.findall(out)
        self.assertTrue(paths, out)
        self.assertNotIn("terraform/environments/aws/dev", out)
        for p in paths:
            p = p.rstrip("/")
            if p.endswith(".tfbackend") or "/.terraform/" in p:
                continue
            self.assertTrue((ROOT / p).exists(), f"{target}: {p} does not exist")

    def test_validate(self):
        self.check("validate")

    def test_state_list(self):
        self.check("state-list")

    def test_state_show(self):
        self.check("state-show", "RESOURCE=x")

    def test_dependabot_lists_docker_compose(self):
        text = (ROOT / ".github" / "dependabot.yml").read_text()
        self.assertIn("package-ecosystem: docker-compose", text)


if __name__ == "__main__":
    unittest.main()
