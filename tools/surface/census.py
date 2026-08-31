"""Which of the platform's exported surface has no test ever executed?

    go test -count=1 -coverpkg=./internal/... -coverprofile=.gotmp/cover.out \
        ./internal/... ./tests/...
    python tools/surface/census.py

The tenth audit ended on its own limit: *"I chose which comparisons to look at. A boundary
in code I did not read is exactly as invisible as an invariant nobody wrote down."* This
chooses nothing. It enumerates every exported function and method in internal/, asks the
coverage profile which ones ran, and asks the repository which ones anything calls.

Three outcomes, and only the first is comfortable:

    executed                    some test has run it
    never executed, has callers a path the platform uses and nothing has ever tried
    never executed, no callers  code that exists and nobody, including the platform, uses

Two limits, both known and neither hidden: a method reached only through an interface is
invisible to a search by name — the three YAML marshallers are called by the decoder and
report as orphans — and a name shared with another symbol reports as called. The list is a
place to look, not a verdict.

The census is over the suites that run without infrastructure. Anything the integration,
chaos or process suites reach is listed as unexecuted here and marked so in the report,
because saying "covered elsewhere" about a suite this host cannot run is the kind of claim
these audits exist to catch.
"""

import json
import re
import subprocess
from collections import defaultdict
from pathlib import Path
from typing import Any

PROFILE = Path(".gotmp/cover.out")
HERE = Path(__file__).parent

FUNC = re.compile(r"^func(?:\s+\(\s*\w+\s+\*?(?P<recv>\w+)\s*\))?\s+(?P<name>[A-Z]\w*)\s*[(\[]")


def covered_lines() -> dict[str, set[int]]:
    ran: dict[str, set[int]] = defaultdict(set)
    for raw in PROFILE.read_text(encoding="utf-8").splitlines()[1:]:
        location, _, count = raw.rpartition(" ")
        block, _, _ = location.partition(" ")
        if int(count) == 0:
            continue
        path, _, span = block.partition(":")
        start, _, end = span.partition(",")
        key = path.split("agentic-assurance/", 1)[-1]
        ran[key].update(range(int(start.split(".")[0]), int(end.split(".")[0]) + 1))
    return ran


def functions() -> list[dict[str, Any]]:
    """Every exported function and method in internal/, with the line range it spans."""
    found: list[dict[str, Any]] = []
    for path in sorted(Path("internal").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        starts: list[tuple[int, str, str]] = []
        for number, line in enumerate(lines, start=1):
            match = FUNC.match(line)
            if match:
                starts.append((number, match.group("name"), match.group("recv") or ""))
        for index, (number, name, recv) in enumerate(starts):
            end = starts[index + 1][0] - 1 if index + 1 < len(starts) else len(lines)
            found.append({
                "file": path.as_posix(), "name": name, "receiver": recv,
                "start": number, "end": end,
            })
    return found


def has_caller(entry: dict[str, Any]) -> bool:
    """Whether anything outside the declaring file names this symbol.

    -E, because the first version used a basic-regex alternation that matched nothing and
    reported 138 of 138 functions as called by nobody. A unanimous result means the
    instrument is broken, not that the codebase is.
    """
    # Fixed strings rather than a regex: an escaped parenthesis has now been mangled twice
    # on the way through this file, and a pattern that fails to compile reports every
    # symbol as uncalled.
    done = subprocess.run(
        ["grep", "-rlF", "--include=*.go", "-e", f".{entry['name']}(",
         "-e", f" {entry['name']}(", "-e", f"\t{entry['name']}(", "."],
        capture_output=True, text=True,
    )
    files = {line.lstrip("./").replace("\\", "/") for line in done.stdout.split() if line}
    if files - {entry["file"]}:
        return True

    # A method used only inside its own file is still used. Excluding the declaring file
    # wholesale reported MarkPublishedBatch, MarkFailed and ObjectKey as orphans while
    # Drain and the exporter call all three, so the file is read instead and the
    # declaration line skipped.
    if entry["file"] in files:
        lines = Path(entry["file"]).read_text(encoding="utf-8").splitlines()
        for number, line in enumerate(lines, start=1):
            if number == entry["start"]:
                continue
            if f".{entry['name']}(" in line or f" {entry['name']}(" in line:
                return True
    return False


def main() -> None:
    ran = covered_lines()
    rows: list[dict[str, Any]] = []
    for entry in functions():
        lines = ran.get(entry["file"], set())
        executed = any(n in lines for n in range(entry["start"], entry["end"] + 1))
        rows.append({**entry, "executed": executed})

    unexecuted = [r for r in rows if not r["executed"]]
    for row in unexecuted:
        row["called_elsewhere"] = has_caller(row)

    orphans = [r for r in unexecuted if not r["called_elsewhere"]]

    (HERE / "last-results.json").write_text(
        json.dumps({
            "exported": len(rows),
            "executed": len(rows) - len(unexecuted),
            "unexecuted": [f"{r['file']}:{r['start']} {r['receiver']}.{r['name']}"
                           for r in unexecuted],
            "unexecuted_and_uncalled": [f"{r['file']}:{r['start']} {r['receiver']}.{r['name']}"
                                        for r in orphans],
        }, indent=2) + "\n", encoding="utf-8", newline="\n")

    print(f"{len(rows)} exported functions and methods in internal/")
    print(f"{len(rows) - len(unexecuted)} executed by a test that runs without infrastructure")
    print(f"{len(unexecuted)} never executed, of which {len(orphans)} are called by nothing:")
    for row in orphans:
        print(f"    {row['file']}:{row['start']}  {row['receiver']}.{row['name']}")


if __name__ == "__main__":
    main()
