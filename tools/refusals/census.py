"""Which refusals does any test actually execute?

    go test -count=1 -coverpkg=./internal/... -coverprofile=.gotmp/cover.out \
        ./internal/... ./tests/...
    python tools/refusals/census.py


The grep census overstated: a test that asserts on a sentinel error never mentions the code
string, so 102 of 154 looked untested when many are not. This asks the suite instead. Go's
coverage profile records which statements ran; a refusal whose own line never ran is a
promise no test has made the platform keep.

Unit and structural suites only — the ones that run without infrastructure. Integration,
chaos and process are excluded here because the daemon is down on this host, and their
absence is the first thing the report says.
"""

import json
import re
from collections import defaultdict
from pathlib import Path

CODE = re.compile(r'"([A-Z][A-Z0-9_]{6,})"')
PROFILE = Path(".gotmp/cover.out")
REFUSAL_MARKERS = (
    "activationErr(", "deny(", "Code:", "errorBody(", "SignatureError{", "ClaimError{",
    "reservationDecision(", "refuse(", "Refusal{", "denyDecision(", "ValidationError{",
)

NOT_A_REFUSAL = set(
    json.loads((Path(__file__).parent / "not-a-refusal.json").read_text(encoding="utf-8"))
)


def covered_lines() -> dict[str, set[int]]:
    """Line numbers that some test executed, per file."""
    ran: dict[str, set[int]] = defaultdict(set)
    for raw in PROFILE.read_text(encoding="utf-8").splitlines()[1:]:
        # name.go:startLine.startCol,endLine.endCol numStmt count
        location, _, count = raw.rpartition(" ")
        block, _, _ = location.partition(" ")
        if int(count) == 0:
            continue
        path, _, span = block.partition(":")
        start, _, end = span.partition(",")
        first = int(start.split(".")[0])
        last = int(end.split(".")[0])
        key = path.split("agentic-assurance/", 1)[-1]
        ran[key].update(range(first, last + 1))
    return ran


def main() -> None:
    ran = covered_lines()
    rows = []
    for path in sorted(Path("internal").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        key = path.as_posix()
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        for number, line in enumerate(lines, start=1):
            code, _, _ = line.partition("//")
            # Only codes in a refusal position. The first pass counted enum values —
            # US_OPEN, SAME_TENANT, PARTIALLY_FILLED — as untested refusals, which is the
            # census lying about what it measured.
            context = code + " " + " ".join(lines[max(0, number - 3):number])
            if not any(marker in context for marker in REFUSAL_MARKERS):
                continue
            for match in CODE.finditer(code):
                name = match.group(1)
                if name in NOT_A_REFUSAL or "_" not in name:
                    continue
                rows.append({
                    "code": name,
                    "at": f"{key}:{number}",
                    "executed": number in ran.get(key, set()),
                })

    by_code: dict[str, list[dict[str, object]]] = defaultdict(list)
    for row in rows:
        by_code[str(row["code"])].append(row)

    never = sorted(c for c, rs in by_code.items() if not any(r["executed"] for r in rs))
    (Path(__file__).parent / "last-results.json").write_text(
        json.dumps({"codes": len(by_code), "never_executed": never}, indent=2), encoding="utf-8")

    print(f"{len(by_code)} refusal codes produced in internal/")
    print(f"{len(by_code) - len(never)} are executed by some test that runs without infrastructure")
    print(f"{len(never)} are never executed:")
    for code in never:
        print("   ", code, "  ", by_code[code][0]["at"])


if __name__ == "__main__":
    main()
