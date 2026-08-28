#!/usr/bin/env sh
# The race detector, in a container.
#
# -race needs cgo, and a Windows development environment without a C compiler cannot
# run it at all. That is why it had never run on this repository: not a decision, an
# absence nobody had priced. This project already requires Docker, so the detector runs
# where a compiler exists.
#
# The simulation tests are excluded. They execute the project's Python interpreter,
# which is a Windows binary the container cannot run; the failure is the mount, not the
# code, and a suite that always fails is a suite people learn to ignore.
set -eu

IMAGE="golang:1.25"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> race detector ($IMAGE)"

# MSYS_NO_PATHCONV keeps Git Bash from rewriting the container paths into Windows ones.
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$ROOT:/src" -w /src \
  -e GOFLAGS=-buildvcs=false \
  -e GOCACHE=/tmp/gocache \
  -e GOMODCACHE=/tmp/gomod \
  "$IMAGE" \
  go test -race $(go list ./... | grep -v 'internal/simulation' | sed 's|^agentic-assurance|.|' | tr '\n' ' ')

echo "Race detector passed."
