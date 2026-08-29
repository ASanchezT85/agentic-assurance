# Web Console Patterns

## Canonical layout
- fixed/compact left navigation;
- 64 px top bar;
- surface header;
- summary region;
- primary operational visualization/table;
- progressive detail drawer.

## Read-only interaction vocabulary
Allowed: inspect, filter, search, sort, expand, compare, copy, navigate, export if API later supports read export.  
Not allowed in V0 Console: authorize, revoke, submit, activate, kill, approve, trade.

## Responsive behavior
- >= 1440: full data workstation.
- 1024-1439: compact sidebar; two-column regions collapse selectively.
- 768-1023: sidebar rail; secondary context becomes drawer.
- < 768 web browser: companion-style summaries; dense tables scroll horizontally or switch to record cards. This does not replace the future native mobile product.

## Availability
Every data region owns one of four explicit states: loading, available (including empty), stale, unavailable.
