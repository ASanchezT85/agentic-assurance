# The simulation-engine deployable (ADR-011 counts four; ADR-016 places this one).
#
# A Python process rooted at simulator/, not a Go binary and not a directory under
# cmd/. Nothing in production depends on it: spec section 17 requires production
# execution to be unaffected when the simulator is unavailable.

FROM python:3.12-slim

WORKDIR /app

COPY pyproject.toml ./
RUN pip install --no-cache-dir "numpy>=2.1"

COPY simulator ./simulator

# A seed is required, not defaulted. An unseeded experiment is not reproducible, and
# a default would make every unseeded run look reproducible until someone compared
# two of them.
ENTRYPOINT ["python", "-m", "simulator.engine"]
CMD ["--help"]
