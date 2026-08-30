"""The mutation sweep: break one enforcement point at a time and see whether anything fails.

    python tools/mutation/sweep.py

A green suite proves nothing about a guarantee it does not exercise. Each entry in
mutations.json removes one thing the platform claims it enforces; the sweep applies them one
at a time to a clean tree and runs the suites the quality gate runs. A mutation nothing
catches is a guarantee with no guard, which is what the seventh audit found for five of
twelve — including the issuer privilege, which had no test at all.

COMMIT FIRST. revert() is `git checkout -- internal/`, which discards uncommitted work: it
deleted a fix mid-sweep once already.

It runs inside the Linux container the race detector already uses. This workstation's Smart
App Control evaluates each freshly written executable and its verdict is per file and
sticky, so a mutated package produces a binary that is blocked and stays blocked however
often it is retried — on the host the sweep measures nothing at all.
"""

import json
import os
import subprocess
from pathlib import Path
from typing import Any

IMAGE = "agentic-assurance-race"
TARGETS = "./internal/... ./tests/security/ ./tests/scenarios/ ./tests/contract/ ./tests"
HERE = Path(__file__).parent


def repo_root() -> str:
    """The path the Docker daemon can mount.

    Git Bash reports a POSIX path the daemon cannot resolve; cygpath gives the Windows one
    where the shell has it, and the POSIX path is correct everywhere else.
    """
    posix = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True
    ).stdout.strip()
    try:
        windows = subprocess.run(
            ["cygpath", "-w", posix], capture_output=True, text=True
        ).stdout.strip()
    except FileNotFoundError:
        return posix
    return windows or posix


ROOT = repo_root()


def in_container(command: str) -> tuple[int, str]:
    """docker run, without a shell.

    The first attempt used shell=True, which on this host is cmd.exe: it does not
    understand a VAR=value prefix, so every container run failed with "MSYS_NO_PATHCONV no
    se reconoce como un comando" and the sweep recorded a dozen compile failures that were
    nothing of the kind.
    """
    env = dict(os.environ, MSYS_NO_PATHCONV="1")
    argv = [
        "docker", "run", "--rm",
        "-v", f"{ROOT}:/src", "-w", "/src",
        "-v", "agentic-assurance-gomod:/tmp/gomod",
        "-v", "agentic-assurance-gocache:/tmp/gocache",
        "-e", "SIMULATOR_REPO=/src",
        IMAGE,
    ] + command.split()
    done = subprocess.run(argv, capture_output=True, text=True, env=env, timeout=2400)
    return done.returncode, done.stdout + done.stderr


def revert() -> None:
    subprocess.run(["git", "checkout", "--", "internal/"], capture_output=True, text=True)


def apply(mutation: dict[str, str]) -> bool:
    path = Path(mutation["file"])
    source = path.read_text(encoding="utf-8")
    if mutation["old"] not in source:
        return False
    path.write_text(
        source.replace(mutation["old"], mutation["new"], 1), encoding="utf-8", newline="\n"
    )
    return True


def unusable(output: str) -> bool:
    """Whether the container could not run at all.

    Docker Desktop stopped in the middle of a sweep once, and every row came back "DID NOT
    COMPILE" — twenty-three of them, including the baseline. A harness that reports a dead
    daemon as a compilation failure is the defect these audits keep finding, one level up,
    which is why this is a case of its own.
    """
    return "docker API" in output or "daemon is running" in output


def write_results(results: list[dict[str, Any]]) -> None:
    body = json.dumps(results, indent=2) + "\n"
    (HERE / "last-results.json").write_text(body, encoding="utf-8", newline="\n")


def failing_packages(output: str) -> list[str]:
    return sorted(
        {line.split()[1] for line in output.splitlines()
         if line.startswith("FAIL\t") and len(line.split()) > 1}
    )


def main() -> None:
    """Run the sweep, and put the tree back whatever happens.

    Twice now a run has ended between applying a mutation and reverting it — once killed on
    a timeout, once interrupted — and left the enforcement point removed in the working
    tree. A harness that can leave the code sabotaged is worse than no harness.
    """
    try:
        sweep()
    finally:
        revert()


def sweep() -> None:
    mutations: list[dict[str, str]] = json.loads(
        (HERE / "mutations.json").read_text(encoding="utf-8")
    )

    # A baseline first: the suite must be green before a mutation means anything.
    code, out = in_container(f"go test -count=1 {TARGETS}")
    baseline: dict[str, Any] = {
        "id": "BASELINE (no mutation)",
        "claim": "the suite is green to begin with",
        "outcome": "green" if code == 0 else "NOT GREEN",
    }
    if code != 0:
        baseline["detail"] = failing_packages(out)
    results: list[dict[str, Any]] = [baseline]
    print(json.dumps(baseline), flush=True)
    if code != 0:
        # Nothing measured after this point would mean anything: a mutation is only
        # interesting against a suite that was green without it.
        if unusable(out):
            baseline["outcome"] = "CONTAINER UNAVAILABLE"
            baseline["detail"] = out.strip().splitlines()[:2]
        write_results(results)
        raise SystemExit(json.dumps(baseline))

    for mutation in mutations:
        revert()
        row: dict[str, Any] = {"id": mutation["id"], "claim": mutation["claim"]}
        if not apply(mutation):
            row["outcome"] = "NOT APPLIED (the anchor moved)"
        else:
            code, out = in_container("go build ./...")
            if code != 0:
                row["outcome"] = "CONTAINER UNAVAILABLE" if unusable(out) else "DID NOT COMPILE"
                row["detail"] = out.strip().splitlines()[:3]
            else:
                code, out = in_container(f"go test -count=1 {TARGETS}")
                if code != 0:
                    row["outcome"] = "caught"
                    row["by"] = failing_packages(out)
                else:
                    row["outcome"] = "NOT CAUGHT"
        results.append(row)
        print(json.dumps(row), flush=True)

    revert()
    write_results(results)


if __name__ == "__main__":
    main()
