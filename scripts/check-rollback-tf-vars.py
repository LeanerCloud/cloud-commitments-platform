#!/usr/bin/env python3
"""Each rollback job's Terraform apply must pass what its deploy workflow passes.

A rollback apply that omits a deploy TF_VAR_ or -var resets that variable on the
live stack (issue #1744), and one with a different source or an extra variable
changes the stack just as silently. For each (rollback job, deploy workflow)
pair this compares, in both directions and by value, every TF_VAR_* env entry,
-var flag and shell-exported TF_VAR_ of the one step that applies.

Shell text is read line by line (continuations joined, split on ; && ||, not
quote-aware). A TF_VAR_ counts from run text only when exported in the step
(`export A=1 B=2`, `export A` after `A=1`, `declare -x`/`typeset -x`) or given
as an inline prefix (`A=1 terraform apply`, `env A=1 terraform apply`) to the
terraform plan or apply command itself. A bare assignment does not count, and
neither does a prefix on another command. Not understood: `export -n`,
`unset`, eval, sourced files, functions, heredoc or quoted-body lines,
variables built from other variables' names. Ordering is not modelled (an
export after the apply still counts), and conditionals (`if ...; then export`)
and `sudo terraform apply` are not recognised; they fail closed with a
'missing in rollback' message.
Any run step that writes a TF_VAR_ to $GITHUB_ENV in a rollback job or a deploy
workflow fails the check outright, since it would reach later steps unseen.
That rule scans only the same step's run text: a script file the step calls, a
name built from variables, or TF_VAR_ and GITHUB_ENV in separate steps are out
of scope. The rollback's ENVIRONMENT must be defined as inputs.environment.

Requires PyYAML (CI installs a pinned version in a venv). The Azure OIDC step in
ci.yml still uses the runner's system python3 for PyYAML; that drift risk is out
of scope here.

The deploy workflows plan in one step (`terraform plan -var ...`) and apply the
saved plan in the next (`terraform apply tfplan`), so the deploy side is the
plan step; a rollback applies directly, so its side is the apply step.

Usage: check-rollback-tf-vars.py [workflows dir]
"""
import re
import sys
from pathlib import Path

import yaml

PAIRS = [
    ("rollback-aws-lambda", "deploy-aws-lambda.yml"),
    ("rollback-aws-fargate", "deploy-aws-fargate.yml"),
    ("rollback-gcp", "deploy-gcp.yml"),
    ("rollback-azure", "deploy-azure.yml"),
]

MISSING_DEPLOY = "extra in rollback"
VALUE = "value differs"
# Keys allowed to differ, with the one difference allowed and why.
EXCEPTIONS = {
    # The rollback pins a previous image; a deploy builds its image elsewhere.
    "-var image_uri": MISSING_DEPLOY,
    # Both read the live function URL, deploy through a step output and the
    # rollback through a shell export of the same script.
    "TF_VAR_lambda_deployed_function_url": VALUE,
}

# Deploys name the environment TARGET_ENVIRONMENT (a prepare-job output) and
# rollbacks name it ENVIRONMENT (inputs.environment); both are the environment
# being deployed. Their definitions legitimately differ, so -var-file text is
# aliased to one name and that name's definitions are not compared.
ENV_ALIASES = {"TARGET_ENVIRONMENT": "ENVIRONMENT"}
ALIASED_NAMES = set(ENV_ALIASES.values())

VAR_FILE = re.compile(r"""-var-file(?:=|\s+)(?:"([^"]*)"|'([^']*)'|(\S+))""")
VAR_FLAG = re.compile(
    r"""-var(?:=|\s+)(?:"([^"=]+)=([^"]*)"|'([^'=]+)=([^']*)'|([^\s"'=]+)=(\S*))"""
)
ASSIGN_VALUE = r"""(?:"[^"]*"|'[^']*'|\$\([^)]*\)|\S*)"""
LEAD_ASSIGN = re.compile(rf"(?:env\s+)?((?:[A-Za-z_]\w*={ASSIGN_VALUE}(?:\s+|$))*)")
ASSIGN_NAME = re.compile(r"([A-Za-z_]\w*)=")
EXPORTED_NAME = re.compile(r"""(?:^|\s)["']?(TF_VAR_\w+)(=)?""")
SEGMENT_SPLIT = re.compile(r"\s*(?:;|&&|\|\|)\s*")
INPUTS_ENV = re.compile(r"\$\{\{\s*(?:github\.event\.)?inputs\.environment\s*\}\}")
ENV_REF = re.compile(r"\$\{?([A-Za-z_]\w*)\}?")


class CheckError(Exception):
    pass


def code_lines(run):
    return [l for l in str(run or "").splitlines() if not l.lstrip().startswith("#")]


def logical_lines(run):
    out, cur = [], ""
    for l in code_lines(run):
        if l.rstrip().endswith("\\"):
            cur += l.rstrip()[:-1] + " "
        else:
            out.append(cur + l)
            cur = ""
    return out + ([cur] if cur else [])


def shell_tf_vars(run):
    """TF_VAR_ names the step's shell text hands to Terraform (see header)."""
    assigned, exported, inline = set(), set(), set()
    for line in logical_lines(run):
        for seg in SEGMENT_SPLIT.split(line):
            lead = LEAD_ASSIGN.match(seg.strip())
            names = set(ASSIGN_NAME.findall(lead.group(1)))
            words = seg.strip()[lead.end():].split()
            if not words:
                assigned |= names
            elif words[0] == "terraform" and ({"plan", "apply"} & set(words)):
                inline |= names
            elif words[0] == "export" or (
                words[0] in ("declare", "typeset") and len(words) > 1 and re.fullmatch(r"-\w*x\w*", words[1])
            ):
                for m in EXPORTED_NAME.finditer(" ".join(words[1:])):
                    exported.add(m.group(1))
                    if m.group(2):
                        assigned.add(m.group(1))
    return {n for n in (assigned & exported) | inline if n.startswith("TF_VAR_")}


def github_env_writes(doc, jobs, where):
    errs = []
    for jname in jobs:
        for step in doc["jobs"][jname].get("steps") or []:
            text = "\n".join(code_lines(step.get("run")))
            if "GITHUB_ENV" in text and "TF_VAR_" in text:
                errs.append(f"{where} {jname}: step '{step.get('name')}' writes a TF_VAR_ to $GITHUB_ENV")
    return errs


def load(path):
    if not path.is_file():
        raise CheckError(f"missing workflow file {path.name}")
    doc = yaml.safe_load(path.read_text())
    if not isinstance(doc, dict) or not isinstance(doc.get("jobs"), dict):
        raise CheckError(f"{path.name} has no jobs")
    return doc


def find_step(doc, jobs, needle, where):
    hits = []
    for jname in jobs:
        for step in doc["jobs"][jname].get("steps") or []:
            if any(needle in l for l in code_lines(step.get("run"))):
                hits.append((jname, step))
    if len(hits) != 1:
        raise CheckError(f"{where}: want exactly one step running '{needle}', found {len(hits)}")
    return hits[0]


def collect(doc, jname, step):
    """Return ({key: value-or-None}, {env name: definition}) for one step."""
    pairs, files = {}, []
    for line in code_lines(step.get("run")):
        for m in VAR_FLAG.finditer(line):
            g = [x for x in m.groups() if x is not None]
            pairs[f"-var {g[0]}"] = g[1]
        for m in VAR_FILE.finditer(line):
            text = next(x for x in m.groups() if x is not None)
            for old, new in ENV_ALIASES.items():
                text = text.replace("${%s}" % old, "${%s}" % new).replace("$" + old, "$" + new)
            files.append(text)
    for name in shell_tf_vars(step.get("run")):
        pairs.setdefault(name, "<shell export>")
    if files:
        pairs["-var-file"] = ",".join(sorted(files))
    # Later scopes override earlier ones, as in GitHub Actions.
    defs = {}
    for scope in (doc.get("env"), doc["jobs"][jname].get("env"), step.get("env")):
        for k, v in (scope or {}).items():
            defs[str(k)] = str(v)
    for k, v in defs.items():
        if k.startswith("TF_VAR_"):
            pairs[k] = v
    return pairs, defs


def compare(job, deploy_file, r, d):
    (rp, rdefs), (dp, ddefs) = r, d
    errs = []
    for key in sorted(set(rp) | set(dp)):
        if key not in dp:
            kind = MISSING_DEPLOY
        elif key not in rp:
            kind = "missing in rollback"
        elif rp[key] != dp[key]:
            kind = VALUE
        else:
            continue
        if EXCEPTIONS.get(key) != kind:
            errs.append(f"{job} vs {deploy_file}: {key}: {kind}")
    for key, rval in sorted(rp.items()):
        if key not in dp or key in EXCEPTIONS:
            continue
        for ref in sorted(set(ENV_REF.findall(rval))):
            if key == "-var-file" and ref in ALIASED_NAMES:
                continue
            if rdefs.get(ref) != ddefs.get(ref):
                errs.append(f"{job} vs {deploy_file}: env {ref} (used by {key}): value differs")
    return errs


def check_pair(wdir, rollback_doc, job, deploy_file):
    if job not in rollback_doc["jobs"]:
        raise CheckError(f"rollback.yml has no job {job}")
    deploy_doc = load(wdir / deploy_file)
    _, rstep = find_step(rollback_doc, [job], "terraform apply", f"rollback.yml {job}")
    find_step(deploy_doc, list(deploy_doc["jobs"]), "terraform apply", deploy_file)
    djob, dstep = find_step(deploy_doc, list(deploy_doc["jobs"]), "terraform plan", deploy_file)
    r = collect(rollback_doc, job, rstep)
    d = collect(deploy_doc, djob, dstep)
    for side, pairs in (("rollback", r[0]), ("deploy", d[0])):
        if "-var-file" not in pairs:
            raise CheckError(f"{job} vs {deploy_file}: {side} passes no -var-file")
    if "-var image_uri" not in r[0]:
        raise CheckError(f"{job}: rollback does not pass -var image_uri")
    if not r[0] or not d[0]:
        raise CheckError(f"{job} vs {deploy_file}: extracted no variables from one side")
    env = r[1].get("ENVIRONMENT")
    errs = [] if env and INPUTS_ENV.fullmatch(env.strip()) else [
        f"{job}: ENVIRONMENT must be ${{{{ inputs.environment }}}}, got {env!r}"
    ]
    errs += github_env_writes(rollback_doc, [job], "rollback.yml")
    errs += github_env_writes(deploy_doc, list(deploy_doc["jobs"]), deploy_file)
    return errs + compare(job, deploy_file, r, d)


def main(argv):
    wdir = Path(argv[1]) if len(argv) > 1 else Path(__file__).resolve().parent.parent / ".github" / "workflows"
    try:
        rollback_doc = load(wdir / "rollback.yml")
        errs = []
        for job, deploy_file in PAIRS:
            errs += check_pair(wdir, rollback_doc, job, deploy_file)
    except CheckError as e:
        print(f"FAIL: {e}", file=sys.stderr)
        return 1
    for e in errs:
        print(f"FAIL: {e}", file=sys.stderr)
    if errs:
        return 1
    print("rollback applies match the deploy applies")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
