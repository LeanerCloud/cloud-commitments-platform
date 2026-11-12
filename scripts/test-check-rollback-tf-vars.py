#!/usr/bin/env python3
"""Self-test for check-rollback-tf-vars.py: each drift probe is applied to a copy
of the real workflows and must fail the check; the unmodified copy must pass."""
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
CHECK = ROOT / "scripts" / "check-rollback-tf-vars.py"
SRC = ROOT / ".github" / "workflows"
NAMES = ["rollback.yml", "deploy-aws-lambda.yml", "deploy-aws-fargate.yml", "deploy-gcp.yml", "deploy-azure.yml"]


def apply_step(rollback, job):
    return next(s for s in rollback["jobs"][job]["steps"] if "terraform apply" in str(s.get("run")))


def probe_source(rb):
    apply_step(rb, "rollback-aws-lambda")["env"]["TF_VAR_from_email"] = "${{ vars.FROM_EMAIL }}"


def probe_extra(rb):
    apply_step(rb, "rollback-aws-fargate")["env"]["TF_VAR_extra"] = "x"


def probe_comment(rb):
    s = apply_step(rb, "rollback-aws-lambda")
    del s["env"]["TF_VAR_from_email"]
    s["run"] = "# TF_VAR_from_email: ${{ secrets.FROM_EMAIL }}\n" + s["run"]


def probe_moved(rb):
    steps = rb["jobs"]["rollback-aws-lambda"]["steps"]
    i = steps.index(apply_step(rb, "rollback-aws-lambda"))
    steps[i - 1].setdefault("env", {})["TF_VAR_dashboard_url"] = steps[i]["env"].pop("TF_VAR_dashboard_url")


def probe_no_project_id(rb):
    s = apply_step(rb, "rollback-gcp")
    s["run"] = "\n".join(l for l in s["run"].splitlines() if "project_id" not in l)


def probe_location_def(rb):
    apply_step(rb, "rollback-azure")["env"]["AZURE_LOCATION"] = "eastus"


def probe_no_location(rb):
    s = apply_step(rb, "rollback-azure")
    s["run"] = "\n".join(l for l in s["run"].splitlines() if "location=" not in l)


def probe_no_apply(rb):
    apply_step(rb, "rollback-gcp")["run"] = "terraform destroy"


def probe_no_job(rb):
    rb["jobs"]["rollback-gcp-renamed"] = rb["jobs"].pop("rollback-gcp")


def strip_lines(job, needle):
    def probe(rb):
        s = apply_step(rb, job)
        s["run"] = "\n".join(l for l in s["run"].splitlines() if needle not in l)
    return probe


def probe_varfile_hardcoded(rb):
    s = apply_step(rb, "rollback-aws-lambda")
    s["run"] = s["run"].replace('github-${ENVIRONMENT}.tfvars', "github-prod.tfvars")


def probe_varfile_template(rb):
    s = apply_step(rb, "rollback-gcp")
    s["run"] = s["run"].replace('github-${ENVIRONMENT}.tfvars', "${ENVIRONMENT}.tfvars")


def probe_job_level(rb):
    rb["jobs"]["rollback-aws-lambda"].setdefault("env", {})["TF_VAR_extra"] = "x"


def probe_workflow_level(rb):
    rb.setdefault("env", {})["TF_VAR_extra"] = "x"


def probe_step_to_job_same(rb):
    s = apply_step(rb, "rollback-aws-lambda")
    v = s["env"].pop("TF_VAR_dashboard_url")
    rb["jobs"]["rollback-aws-lambda"].setdefault("env", {})["TF_VAR_dashboard_url"] = v


def probe_step_to_job_diff(rb):
    probe_step_to_job_same(rb)
    rb["jobs"]["rollback-aws-lambda"]["env"]["TF_VAR_dashboard_url"] = "other"


LAMBDA_URL = "TF_VAR_lambda_deployed_function_url"
URL_CMD = "$(../../../scripts/read-lambda-function-url.sh .)"


def rewrite_run(job, old, new):
    def probe(rb):
        s = apply_step(rb, job)
        assert old in s["run"], old
        s["run"] = s["run"].replace(old, new)
    return probe


EXPORT_LINE = f"export {LAMBDA_URL}\n"
ASSIGN_LINE = f"{LAMBDA_URL}={URL_CMD}\n"
APPLY = "terraform apply -auto-approve"


def lambda_run(rb, fn):
    s = apply_step(rb, "rollback-aws-lambda")
    s["run"] = fn(s["run"])


def probe_no_export(rb):
    lambda_run(rb, lambda r: r.replace(EXPORT_LINE, ""))


def probe_export_other(rb):
    lambda_run(rb, lambda r: r.replace(EXPORT_LINE, "export TF_VAR_other\n"))


def probe_inline_other_cmd(rb):
    lambda_run(rb, lambda r: r.replace(EXPORT_LINE, "").replace("terraform init", f"{LAMBDA_URL}=x terraform init"))


def probe_export_one_line(rb):
    lambda_run(rb, lambda r: r.replace(ASSIGN_LINE + EXPORT_LINE, f"export {LAMBDA_URL}={URL_CMD}\n"))


def probe_export_multi(rb):
    lambda_run(rb, lambda r: r.replace(ASSIGN_LINE + EXPORT_LINE, f"A=1\nexport A {LAMBDA_URL}=$(x) B=2\n"))


def probe_declare_x(rb):
    lambda_run(rb, lambda r: r.replace(EXPORT_LINE, f"declare -x {LAMBDA_URL}\n"))


def probe_inline_apply(rb):
    lambda_run(rb, lambda r: r.replace(ASSIGN_LINE + EXPORT_LINE, "").replace(APPLY, f"{LAMBDA_URL}={URL_CMD} {APPLY}"))


def probe_env_inline_apply(rb):
    lambda_run(rb, lambda r: r.replace(ASSIGN_LINE + EXPORT_LINE, "").replace(APPLY, f"env {LAMBDA_URL}=x {APPLY}"))


def probe_semicolon_export(rb):
    lambda_run(rb, lambda r: r.replace(ASSIGN_LINE + EXPORT_LINE, f"{LAMBDA_URL}=x; export {LAMBDA_URL}\n"))


def add_github_env_step(rb):
    steps = rb["jobs"]["rollback-aws-lambda"]["steps"]
    steps.insert(0, {"name": "x", "run": 'echo "TF_VAR_extra=1" >> "$GITHUB_ENV"'})


def probe_github_env_deploy(doc):
    doc["jobs"]["build-and-deploy"]["steps"].insert(0, {"name": "x", "run": 'echo "TF_VAR_x=1" >> "$GITHUB_ENV"'})


def probe_env_hardcoded(rb):
    apply_step(rb, "rollback-aws-lambda")["env"]["ENVIRONMENT"] = "prod"


def probe_env_job_level(rb):
    del apply_step(rb, "rollback-gcp")["env"]["ENVIRONMENT"]
    rb["jobs"]["rollback-gcp"].setdefault("env", {})["ENVIRONMENT"] = "${{ vars.DEPLOY_ENV }}"


def probe_env_event_inputs(rb):
    apply_step(rb, "rollback-gcp")["env"]["ENVIRONMENT"] = "${{ github.event.inputs.environment }}"


def probe_env_missing(rb):
    del apply_step(rb, "rollback-azure")["env"]["ENVIRONMENT"]


PASSES = {
    "TF_VAR_ moved step -> job, same value": probe_step_to_job_same,
    "export NAME=value on one line": probe_export_one_line,
    "export A NAME=v B=2 multi-name": probe_export_multi,
    "declare -x NAME": probe_declare_x,
    "inline NAME=v terraform apply": probe_inline_apply,
    "env NAME=v terraform apply": probe_env_inline_apply,
    "assignment; export NAME": probe_semicolon_export,
    "ENVIRONMENT from github.event.inputs": probe_env_event_inputs,
}

MUTATIONS = {
    "TF_VAR_ at job level": probe_job_level,
    "TF_VAR_ at workflow level": probe_workflow_level,
    "TF_VAR_ moved step -> job, new value": probe_step_to_job_diff,
    "-var-file dropped": strip_lines("rollback-aws-fargate", "-var-file"),
    "-var-file hardcoded to prod": probe_varfile_hardcoded,
    "-var-file template changed": probe_varfile_template,
    "image_uri removed": strip_lines("rollback-azure", "image_uri"),
    "TF_VAR_ source secrets. -> vars.": probe_source,
    "rollback-only TF_VAR_extra": probe_extra,
    "deploy var only in a comment": probe_comment,
    "var moved to another step": probe_moved,
    "-var project_id deleted (gcp)": probe_no_project_id,
    "AZURE_LOCATION defined differently": probe_location_def,
    "-var location deleted (azure)": probe_no_location,
    "missing apply step": probe_no_apply,
    "missing rollback job": probe_no_job,
    "TF_VAR_ assigned but never exported": probe_no_export,
    "export of a different TF_VAR_": probe_export_other,
    "inline TF_VAR_ on a non-terraform-apply command": probe_inline_other_cmd,
    "TF_VAR_ written to GITHUB_ENV (rollback)": add_github_env_step,
    "ENVIRONMENT hard-coded to prod": probe_env_hardcoded,
    "ENVIRONMENT from vars at job level": probe_env_job_level,
    "ENVIRONMENT undefined in the apply scope": probe_env_missing,
}

DEPLOY_MUTATIONS = {
    "TF_VAR_ written to GITHUB_ENV (deploy)": ("deploy-aws-lambda.yml", probe_github_env_deploy),
}


def run(mutate=None, drop=None, target="rollback.yml"):
    with tempfile.TemporaryDirectory() as d:
        for n in NAMES:
            shutil.copy(SRC / n, d)
        if drop:
            (Path(d) / drop).unlink()
        if mutate:
            p = Path(d) / target
            doc = yaml.safe_load(p.read_text())
            mutate(doc)
            p.write_text(yaml.safe_dump(doc))
        r = subprocess.run([sys.executable, str(CHECK), d], capture_output=True, text=True)
        # A crash also exits 1; only a reported FAIL counts as the check failing.
        if r.returncode == 1 and not r.stderr.startswith("FAIL:"):
            raise SystemExit(f"check crashed instead of reporting: {r.stderr}")
        if VERBOSE and r.returncode:
            print(f"  [{mutate.__name__ if mutate else drop}] {r.stderr.strip()}")
        return r.returncode


VERBOSE = "-v" in sys.argv
failed = []
if run() != 0:
    failed.append("real tree must pass")
for label, fn in MUTATIONS.items():
    if run(fn) != 1:
        failed.append(f"must fail: {label}")
for label, (target, fn) in DEPLOY_MUTATIONS.items():
    if run(fn, target=target) != 1:
        failed.append(f"must fail: {label}")
for label, fn in PASSES.items():
    if run(fn) != 0:
        failed.append(f"must pass: {label}")
for n in NAMES[1:]:
    if run(drop=n) != 1:
        failed.append(f"must fail: missing {n}")
# Issue #1773: the check imports PyYAML, so CI must install an exact version.
ci = yaml.safe_load((ROOT / ".github" / "workflows" / "ci.yml").read_text())
ci_runs = [str(st.get("run")) for j in ci["jobs"].values() for st in j.get("steps") or []]
ci_step = next((r for r in ci_runs if "check-rollback-tf-vars.py" in r), "")
if not re.search(r"pyyaml==\d+(\.\d+)+", ci_step, re.I):
    failed.append("ci.yml step running check-rollback-tf-vars.py must pip install pyyaml==<exact version>")
for f in failed:
    print(f"FAIL: {f}", file=sys.stderr)
if failed:
    sys.exit(1)
print("check-rollback-tf-vars self-test passed")
