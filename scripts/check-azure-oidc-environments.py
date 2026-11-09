#!/usr/bin/env python3
"""Every environment an azure/login job binds must have a federated credential.

A job with `environment: <name>` presents the OIDC subject `...:environment:<name>`,
so Azure rejects it with AADSTS70021 unless the ci-cd-permissions module lists
<name> in `var.github_environments` (issue #1750: migrate-azure binds
azure-db-<env>, which the default did not list). For each job that runs
`azure/login` this resolves the environment names the job can present and
requires each one in the default of `variable "github_environments"`.

An environment is resolved from a literal, or from an expression made only of
`inputs.environment` / `needs.<job>.outputs.target_environment` plus literal
text, by substituting every option of the workflow's `environment` input
declared as a `choice` under workflow_dispatch. This ASSUMES the workflow's own
validation restricts the value to that same list (deploy-azure.yml's `case
dev|staging|prod`, database-migration.yml's validate job); a shell allowlist that
diverges from the options is outside what a static check can see. Anything else
fails loudly so a new binding style cannot pass unchecked. GitHub settings (the
main-only deployment branch policy each name needs) are not checked.

Usage: check-azure-oidc-environments.py [WORKFLOWS_DIR VARIABLES_TF]
Exit: 0 covered, 1 a bound name has no credential, 2 unresolvable or malformed input
(including no azure/login job at all, which would make the check vacuous).
"""
import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
EXPR = re.compile(r"\$\{\{\s*(.*?)\s*\}\}")
KNOWN_REF = re.compile(r"inputs\.environment|needs\.[\w-]+\.outputs\.target_environment")
DEFAULT_LINE = re.compile(r"^\s*default\s*=\s*\[", re.M)
HCL_COMMENT = re.compile(r"(?:#|//).*")
LIST_BODY = re.compile(r'((?:\s*"[^"]*"\s*,?)*)\s*\]')


class CheckError(Exception):
    pass


def read(path):
    if not path.is_file():
        raise CheckError(f"missing input file {path}")
    return path.read_text()


def default_list(tf):
    m = re.search(r'^variable\s+"github_environments"\s*\{(.*?)^\}', tf, re.M | re.S)
    if not m:
        raise CheckError('variables.tf has no variable "github_environments" block')
    block = m.group(1)
    hits = list(DEFAULT_LINE.finditer(block))
    if len(hits) != 1:
        raise CheckError(f"want exactly one default list in github_environments, found {len(hits)}")
    body = "\n".join(HCL_COMMENT.sub("", l) for l in block[hits[0].end():].splitlines())
    m = LIST_BODY.match(body)
    if not m:
        raise CheckError("github_environments default is not a closed list of strings")
    return set(re.findall(r'"([^"]*)"', m.group(1)))


def dispatch_options(doc):
    triggers = doc.get("on", doc.get(True)) or {}
    inp = ((triggers.get("workflow_dispatch") or {}).get("inputs") or {}).get("environment") or {}
    if inp.get("type") != "choice" or not inp.get("options"):
        return None
    return [str(o) for o in inp["options"]]


def resolve(doc, name, where):
    """Return the set of environment names a job's `environment` can present."""
    if "${{" not in name:
        return {name}
    for expr in EXPR.findall(name):
        if not KNOWN_REF.fullmatch(expr):
            raise CheckError(f"{where}: cannot resolve environment expression '{name}'; add it to the checker")
    options = dispatch_options(doc)
    if not options:
        raise CheckError(f"{where}: environment '{name}' needs a workflow_dispatch choice input 'environment'")
    return {EXPR.sub(lambda _m: opt, name) for opt in options}


def azure_jobs(doc):
    for jname, job in doc["jobs"].items():
        if not isinstance(job, dict):
            raise CheckError(f"job {jname} is not a mapping")
        steps = job.get("steps") or []
        if not isinstance(steps, list) or not all(isinstance(s, dict) for s in steps):
            raise CheckError(f"job {jname}: steps must be a list of mappings")
        if any(str(s.get("uses", "")).strip().lower().startswith("azure/login") for s in steps):
            yield jname, job


def bound_names(wdir):
    """Yield (workflow, job, environment name) for every azure/login job."""
    if not wdir.is_dir():
        raise CheckError(f"workflows directory {wdir} not found")
    for path in sorted([*wdir.glob("*.yml"), *wdir.glob("*.yaml")]):
        try:
            doc = yaml.safe_load(path.read_text())
        except yaml.YAMLError as e:
            raise CheckError(f"{path.name} is not valid YAML: {e}")
        if not isinstance(doc, dict) or not isinstance(doc.get("jobs"), dict):
            continue
        for jname, job in azure_jobs(doc):
            where = f"{path.name} {jname}"
            env = job.get("environment")
            if isinstance(env, dict):
                env = env.get("name")
            if not env:
                raise CheckError(f"{where}: azure/login job has no environment, so no subject the checker can name")
            for name in sorted(resolve(doc, str(env), where)):
                yield path.name, jname, name


def main(argv):
    if len(argv) not in (1, 3):
        print("usage: check-azure-oidc-environments.py [WORKFLOWS_DIR VARIABLES_TF]", file=sys.stderr)
        return 2
    wdir = Path(argv[1]) if len(argv) == 3 else ROOT / ".github" / "workflows"
    tf = Path(argv[2]) if len(argv) == 3 else ROOT / "terraform/environments/azure/ci-cd-permissions/variables.tf"
    try:
        covered = default_list(read(tf))
        bound = list(bound_names(wdir))
        if not bound:
            raise CheckError(f"no azure/login job found in {wdir}; the check would be vacuous")
    except CheckError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 2
    missing = [b for b in bound if b[2] not in covered]
    for wf, job, name in missing:
        print(f"FAIL: {wf} {job}: environment '{name}' has no federated credential in github_environments", file=sys.stderr)
    if missing:
        return 1
    print(f"all {len({b[2] for b in bound})} environments bound by azure/login jobs have a federated credential")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
