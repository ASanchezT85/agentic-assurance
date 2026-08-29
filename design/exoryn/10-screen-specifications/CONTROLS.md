# Controls Screen Specification

## Purpose
Show what currently binds and what historical/shadow controls mean.

## Current source
`GET /v1/controls`.

## V1 hierarchy
- in-force controls first;
- scope, action, authorizer, expiration;
- revoked/expired history;
- explanatory distinction between recommendation and binding control.

## Mandatory
- exact scope;
- read `in_force` from gateway, do not recompute from client clock;
- no authorize/revoke/kill-switch button in web Console;
- whole-tenant wording only when the API truly represents tenant-wide scope.

## Master-Spec gap
The Master Spec describes active policy bundle, grant state and audit history as part of Controls. Current Console API does not expose all of those as a unified read model. Do not fake those panels; add them only after the backend read contract exists.
