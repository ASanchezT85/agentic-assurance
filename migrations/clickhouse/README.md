# ClickHouse migrations

One statement per file, applied in filename order. ClickHouse's HTTP interface takes
a single statement per request, and splitting a multi-statement file in shell is the
kind of parsing that works until a semicolon appears inside a comment.

ClickHouse is analytical only. Spec section 59 forbids it from the synchronous
hard-policy path and ADR-021 says losing it degrades analytics and nothing else, so
nothing defined here is on the critical path and no enforcement decision reads it.
