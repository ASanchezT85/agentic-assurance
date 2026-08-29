# Core Component Contracts

## AppShell
Persistent EXORYN product shell. Contains master logo, six V0 navigation items, tenant context, source/freshness status and user identity if available. No action implying financial mutation.

## SideNav
Exactly six V0 routes: Fleet, Flow, Dependencies, Incidents, Lab, Controls. Selected route uses Electric Blue + Ice background. Do not add “Dashboard” as a seventh principal surface; `/` may route to Fleet or act as a non-principal landing shell.

## SurfaceHeader
Title, one-sentence factual summary, freshness/availability indicator and optional inspect/filter controls.

## MetricCard
One primary value, unit, observation window and optional provenance/coverage. Never show a risk metric without required confidence/coverage context.

## Badge
Compact categorical state. Must include text. Never color-only.

## DataTable
Dense operator table with sticky header, row hover, sortable columns where data contract supports it, horizontal overflow and exact values. Tables are not removed merely because a chart exists.

## SearchField
For correlation IDs, envelope IDs, incident IDs etc. Search/inspection is allowed in read-only Console.

## FilterBar
Client/read-only filters only. Must not imply server-side policy mutation.

## DetailsDrawer
Progressive disclosure for raw payload, provenance, timestamps, hashes and causation. Keyboard dismissible and focus trapped while modal.

## EmptyState
Source responded successfully but had no records. Neutral presentation. Never uses warning copy.

## UnavailableState
Source cannot be trusted/read. Warning presentation, explicit reason, no fake zero values.

## StaleState
Data is present but outside freshness expectation. Timestamp is mandatory.

## CopyableIdentifier
Mono ID with copy affordance and full value in accessible label/tool tip.
