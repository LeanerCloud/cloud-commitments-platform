#!/usr/bin/env python3
"""Self-test for check-azure-oidc-environments.py: each probe is applied to a copy
of the real workflows and variables.tf and must give the expected exit code and
message; the unmodified copy must pass. A probe may return the checker's argument
list; by default it gets the two copied paths."""
import copy
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts" / "check-azure-oidc-environments.py"
WORKFLOWS = ROOT / ".github" / "workflows"
VARIABLES = ROOT / "terraform/environments/azure/ci-cd-permissions/variables.tf"
LOGIN = {"name": "Azure Login", "uses": "azure/login@v3"}


def edit_yaml(name, fn):
    def probe(wdir, tf):
        p = wdir / name
        doc = yaml.safe_load(p.read_text())
        before = copy.deepcopy(doc)
        fn(doc)
        assert doc != before, f"probe did not change {name}"
        p.write_text(yaml.safe_dump(doc))
    return probe


def edit_tf(fn):
    def probe(wdir, tf):
        old = tf.read_text()
        new = fn(old)
        assert new != old, "probe did not change variables.tf"
        tf.write_text(new)
    return probe


def set_env(job, value):
    return lambda doc: doc["jobs"][job].__setitem__("environment", value)


def stage_job(doc):
    return next(j for j, v in doc["jobs"].items() if any(str(s.get("uses", "")).startswith("azure/login") for s in v.get("steps", [])))


def rename_staging(doc):
    doc["jobs"][stage_job(doc)]["environment"] = "stagin"


def recase_login(doc):
    job = doc["jobs"][stage_job(doc)]
    job["environment"] = "stagin"
    for s in job["steps"]:
        if str(s.get("uses", "")).startswith("azure/login"):
            s["uses"] = "Azure/Login@v3"


def bad_job(doc):
    doc["jobs"]["not-a-job"] = []


def bad_step(doc):
    doc["jobs"][stage_job(doc)]["steps"].append("not a mapping")


def drop_dispatch(doc):
    triggers = doc["on"] if "on" in doc else doc[True]
    del triggers["workflow_dispatch"]


def default_with(body):
    return edit_tf(lambda t: re.sub(r"^  default\s*=\s*\[.*\]$", lambda _m: "  default = [" + body + "]", t, count=1, flags=re.M))


def single_arg(wdir, tf):
    return [str(wdir)]


def missing_wdir(wdir, tf):
    return [str(wdir / "nope"), str(tf)]


def missing_tf(wdir, tf):
    return [str(wdir), str(tf.parent / "gone.tf")]


def no_login_jobs(wdir, tf):
    for p in wdir.glob("*.yml"):
        p.unlink()
    (wdir / "plain.yml").write_text("on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: echo\n")


def yaml_extension(wdir, tf):
    (wdir / "database-migration.yml").rename(wdir / "database-migration.yaml")
    tf.write_text(drop_prod(tf.read_text()))


def invalid_yaml(wdir, tf):
    (wdir / "broken.yml").write_text("a: [\n")


def add_no_env_job(doc):
    doc["jobs"]["login-without-environment"] = {"runs-on": "ubuntu-latest", "steps": [LOGIN]}


def drop_prod(text):
    return text.replace(', "azure-db-prod"]', "]")


def zero_defaults(text):
    return re.sub(r"^(\s*)default(\s*)=(\s*)\[", r"\1defaults\2=\3[", text, flags=re.M)


def two_defaults(text):
    return text.replace("  type        = list(string)\n", '  type        = list(string)\n  default     = ["dev"]\n', 1)


# label -> (probe, expected exit code, text the output must contain)
PROBES = {
    "unmodified copy": (lambda w, t: None, 0, "federated credential"),
    "azure-db-prod removed from the default": (edit_tf(drop_prod), 1, "azure-db-prod"),
    "bound environment renamed": (edit_yaml("cleanup-staging.yml", rename_staging), 1, "'stagin'"),
    "unresolvable expression": (edit_yaml("database-migration.yml", set_env("migrate-azure", {"name": "${{ github.ref_name }}"})), 2, "cannot resolve"),
    "azure/login job with no environment": (edit_yaml("cleanup-staging.yml", add_no_env_job), 2, "no environment"),
    "default list matches zero times": (edit_tf(zero_defaults), 2, "found 0"),
    "default list matches twice": (edit_tf(two_defaults), 2, "found 2"),
    "single argument": (single_arg, 2, "usage"),
    "missing workflows dir": (missing_wdir, 2, "not found"),
    "no azure/login job": (no_login_jobs, 2, "vacuous"),
    ".yaml workflow file": (yaml_extension, 1, "database-migration.yaml"),
    "Azure/Login casing": (edit_yaml("cleanup-staging.yml", recase_login), 1, "'stagin'"),
    "commented-out names in the list": (default_with('"dev", "staging", "prod",\n  # "azure-db-dev", "azure-db-staging", "azure-db-prod"\n'), 1, "azure-db-dev"),
    "commented-out names in the list, // style": (default_with('"dev", "staging", "prod",\n  // "azure-db-dev", "azure-db-staging", "azure-db-prod"\n'), 1, "azure-db-dev"),
    "] inside a comment in the list": (default_with('"dev", "staging", "prod", # see [x]\n  "azure-db-dev", "azure-db-staging", "azure-db-prod"\n'), 0, "federated credential"),
    "unclosed list": (edit_tf(lambda t: re.sub(r"^  default\s*=\s*\[.*\]$", '  default = ["dev"', t, count=1, flags=re.M)), 2, "closed list"),
    "missing variables.tf": (missing_tf, 2, "missing input file"),
    "expression without a dispatch choice": (edit_yaml("database-migration.yml", drop_dispatch), 2, "needs a workflow_dispatch choice"),
    "invalid YAML": (invalid_yaml, 2, "not valid YAML"),
    "non-mapping job": (edit_yaml("cleanup-staging.yml", bad_job), 2, "is not a mapping"),
    "non-mapping step": (edit_yaml("cleanup-staging.yml", bad_step), 2, "steps must be"),
}


def run(probe):
    with tempfile.TemporaryDirectory() as d:
        wdir = Path(d) / "workflows"
        shutil.copytree(WORKFLOWS, wdir, ignore=shutil.ignore_patterns("*.md"))
        tf = Path(d) / "variables.tf"
        shutil.copy(VARIABLES, tf)
        args = probe(wdir, tf) or [str(wdir), str(tf)]
        r = subprocess.run([sys.executable, str(CHECK), *args], capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr


failed = []
for label, (probe, want_code, want_text) in PROBES.items():
    code, out = run(probe)
    if code != want_code or want_text not in out:
        failed.append(f"{label}: want exit {want_code} with '{want_text}', got exit {code}: {out.strip()}")
for f in failed:
    print(f"FAIL: {f}", file=sys.stderr)
if failed:
    sys.exit(1)
print("check-azure-oidc-environments self-test passed")
