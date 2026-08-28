# The image the race detector runs in.
#
# -race needs cgo, and a Windows development environment without a C compiler cannot
# run it at all. That is why it had never run on this repository: not a decision, an
# absence nobody had priced.
#
# numpy is here because the Digital Twin is a Python process the simulation tests
# execute, and without it those tests skip — taking the watchdog and the cross-replica
# cancellation, the most concurrent code in the repository, out of the one tool that
# checks concurrency. Skipping them was the first version of this and it hid exactly
# what the detector is for.
FROM golang:1.25

RUN apt-get -qq update \
 && apt-get -qq install -y --no-install-recommends python3 python3-numpy \
 && rm -rf /var/lib/apt/lists/*

ENV GOFLAGS=-buildvcs=false \
    GOCACHE=/tmp/gocache \
    GOMODCACHE=/tmp/gomod \
    SIMULATOR_PYTHON=/usr/bin/python3
