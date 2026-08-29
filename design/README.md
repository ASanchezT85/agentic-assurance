# Design

Product design systems maintained with this repository.

## EXORYN Product Design System V1

`design/exoryn/`

Authority: `design/exoryn/DESIGN_AUTHORITY.md`
Tokens: `design/exoryn/02-tokens/design-tokens.json`
Specification: `design/exoryn/EXORYN_PRODUCT_DESIGN_SYSTEM_V1.pdf`

It sits under the corporate identity in `brand/exoryn/`: the brand system defines the
masterbrand, the logo and the palette; this defines how those constraints appear in
software.

## What this is not

A design specification is not an instruction to change the product. The package says so
itself, and it is repeated here because the distinction is easy to lose once the files
are in the tree:

> This package is a design specification, not authorization to modify the remediation
> branch. Implementation into `apps/console-web` should happen in a separate UI
> integration pass after explicit approval.

`apps/console-web` was not touched by this import. Nothing here is wired into the product
yet.

## Verified at import

Recorded because a design system that quietly diverges from the brand it claims to
implement is the failure mode this directory exists to prevent.

- **The package's own `ASSET_MANIFEST.sha256` verifies.** 56 files, no mismatches, none
  missing.
- **The logo files it carries are byte-identical to the brand masters.**
  `exoryn-icon-primary.svg`, `exoryn-primary-horizontal.svg` and
  `exoryn-circuit-light.svg` match `brand/exoryn/` exactly. A design package shipping a
  subtly different logo is how a brand drifts in the direction nobody approved.
- **The palette does not redefine the brand's.** The eleven brand colours appear
  unchanged. The sixteen additional values are darker text derivatives declared in
  `01-foundations/COLOR.md` for WCAG contrast, which the document states explicitly does
  not redefine the master palette.
- **No font binaries, no operating-system metadata, no nested archive.**
- **The tokens parse** as JSON, the canonical SVGs parse as XML, and the specification PDF
  is a PDF of non-zero length.

## Sample data

Every figure in `mockups/` and `prototype/` is illustrative. None of it is a measured
platform result, and none of it should be quoted as one.
