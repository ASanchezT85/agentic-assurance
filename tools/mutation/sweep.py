"""The mutation sweep: break one enforcement point at a time and see whether anything fails.

    python tools/mutation/sweep.py

A green suite proves nothing about a guarantee it does not exercise. Each entry in
mutations.json removes one thing the platform claims it enforces; the sweep applies them one
at a time to a clean tree and runs the suites the quality gate runs. A mutation nothing
catches is a guarantee with no guard, which is what the seventh audit found for five of the
twelve — including the issuer privilege, which had no test at all.

COMMIT FIRST. revert() is `git checkout -- internal/`, which discards uncommitted work: it
deleted a fix mid-sweep once already.

It runs inside the Linux container the race detector already uses.

Smart App Control on this workstation evaluates every freshly written executable and its
verdict is per file and sticky, so a mutated package produces a binary that is blocked and
stays blocked however many times it is retried. The container has no such policy, and the
repository is already mounted there for the race detector.

Same sweep, same mutations, somewhere the binaries can run.
"""
import io
import json
import os
import subprocess

IMAGE = "agentic-assurance-race"
ROOT = subprocess.run("cygpath -w %s" % subprocess.run(
    "git rev-parse --show-toplevel", shell=True, capture_output=True, text=True).stdout.strip(),
    shell=True, capture_output=True, text=True).stdout.strip()

TARGETS = "./internal/... ./tests/security/ ./tests/scenarios/ ./tests/contract/ ./tests"

MUTATIONS = json.load(io.open("tools/mutation/mutations.json", encoding="utf-8"))


def sh(cmd, timeout=2400):
    p = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=timeout)
    return p.returncode, p.stdout + p.stderr


def in_container(command):
    """docker run, without a shell.

    The first attempt used shell=True, which on this host is cmd.exe: it does not
    understand a VAR=value prefix, so every container run failed with "MSYS_NO_PATHCONV no
    se reconoce como un comando" and the sweep recorded a dozen compile failures that were
    nothing of the kind. A list argv and an explicit env instead.
    """
    env = dict(os.environ, MSYS_NO_PATHCONV="1")
    argv = ["docker", "run", "--rm",
            "-v", "%s:/src" % ROOT, "-w", "/src",
            "-v", "agentic-assurance-gomod:/tmp/gomod",
            "-v", "agentic-assurance-gocache:/tmp/gocache",
            "-e", "SIMULATOR_REPO=/src",
            IMAGE] + command.split()
    p = subprocess.run(argv, capture_output=True, text=True, env=env, timeout=2400)
    return p.returncode, p.stdout + p.stderr


def revert():
    sh("git checkout -- internal/")


def apply(m):
    s = io.open(m["file"], encoding="utf-8").read()
    if m["old"] not in s:
        return False
    io.open(m["file"], "w", encoding="utf-8", newline="\n").write(s.replace(m["old"], m["new"], 1))
    return True


def main():
    # A baseline first: the suite must be green before a mutation means anything.
    code, out = in_container("go test -count=1 %s" % TARGETS)
    baseline = {"id": "BASELINE (no mutation)", "claim": "the suite is green to begin with",
                "outcome": "green" if code == 0 else "NOT GREEN"}
    if code != 0:
        baseline["detail"] = [l for l in out.splitlines() if l.startswith("FAIL")][:5]
    results = [baseline]
    print(json.dumps(baseline), flush=True)

    for m in MUTATIONS:
        revert()
        row = {"id": m["id"], "claim": m["claim"]}
        if not apply(m):
            row["outcome"] = "NOT APPLIED (the anchor moved)"
        else:
            code, out = in_container("go build ./...")
            if code != 0:
                row["outcome"] = "DID NOT COMPILE"
                row["detail"] = out.strip().splitlines()[:3]
            else:
                code, out = in_container("go test -count=1 %s" % TARGETS)
                if code != 0:
                    row["outcome"] = "caught"
                    row["by"] = sorted({l.split()[1] for l in out.splitlines()
                                        if l.startswith("FAIL\t") and len(l.split()) > 1})
                else:
                    row["outcome"] = "NOT CAUGHT"
        results.append(row)
        print(json.dumps(row), flush=True)

    revert()
    io.open("tools/mutation/last-results.json", "w", encoding="utf-8").write(
        json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
