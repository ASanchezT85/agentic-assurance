# Flow Screen Specification

## Purpose
Inspect recent intents and reconstruct one append-only evidence chain.

## Current sources
`GET /v1/intents?limit=50` and `GET /v1/evidence?correlation_id=...`.

## V1 hierarchy
- recent intent table/list on the left or primary region;
- selected intent summary;
- evidence timeline;
- raw payload/details drawer.

## Mandatory semantics
- display the recorded outcome/code, never reinterpret it in UI;
- correction event points to original;
- reconciled outcome says reconciled;
- event producer, sequence and causation remain inspectable;
- BUY/SELL is neutral, not green/red.

## Read-only
Search, inspect and copy are allowed. No order actions.
