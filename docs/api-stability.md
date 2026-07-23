# API Stability, Versioning, and Deprecation

This document defines how KPro intends to communicate compatibility. It does not turn the current repository state into a stable release.

## Current maturity

The repository currently has no Git release tags and no published `v1` compatibility guarantee. Treat the module as pre-1.0 software:

- pin a reviewed commit when consuming it outside this checkout;
- read the diff before updating that pin;
- expect exported APIs to change while the architecture is refined;
- keep KPro-specific construction and adapter details at your application's platform boundary.

Commands such as `go get github.com/saramand9/bignet/kpro@latest` are therefore not the recommended reproducible path today. The [Quick Start](quickstart.md) runs from a checkout for this reason.

## Versioning policy

Tagged releases will follow Semantic Versioning:

- `v0.MINOR.PATCH`: a minor release may contain breaking API changes; a patch release should remain source compatible except for urgent security or correctness repairs.
- `v1.MINOR.PATCH` and later: a major version changes for intentional incompatible public API changes, a minor version adds compatible functionality, and a patch version contains compatible fixes.
- Go module major-version path rules apply at `v2` and later.

A tag, release notes, and an upgrade note are the release contract. A branch name or arbitrary commit is not a compatibility promise.

## Compatibility surface

After `v1`, the compatibility commitment covers documented exported identifiers in packages intended for application use, including `core`, `server`, `transport`, `middleware`, `codec`, `observability`, `database`, and `eventing` subpackages.

The following are not stable API:

- anything under an `internal` directory;
- unexported identifiers;
- test helpers, benchmark fixtures, generated temporary artifacts, and example implementation details;
- undocumented behavior that contradicts an exported contract;
- performance characteristics not stated as a measured release guarantee.

Examples and documentation are compatibility signals: CI should continue to compile them, but copying an example's private application structure does not make that structure framework API.

After `v1`, documented `cmd/kpro` subcommands/flags and the versioned
`cmd/benchguard` budget-file schema are supported tooling interfaces. The exact
layout and implementation inside newly generated source may evolve; review and
commit generated files rather than regenerating over an existing service.

Behavioral compatibility matters alongside compilation. Changes to routing order, cancellation, middleware order, lifecycle ownership, default limits, wire encoding, or error classification must be called out even when function signatures do not change.

## Deprecation policy

When a public API has a replacement, maintainers should:

1. Add a Go `Deprecated:` doc comment that names the replacement.
2. Update examples and primary documentation to use the replacement.
3. Add an upgrade note covering behavioral differences and mechanical migration.
4. Keep the old API working for at least one subsequent minor release after `v1`, unless a critical security, data-loss, or correctness risk makes that unsafe.
5. Remove it only in a release whose version permits the incompatible change.

During `v0`, the same sequence is best effort, but a minor release may shorten the window. Patch releases should not silently remove an exported identifier.

Security and correctness exceptions must be explicit in release notes and should include a safe migration or mitigation whenever possible.

## Release checklist

Before creating a tag, maintainers should:

- run formatting, unit, race-sensitive, protocol, database integration, and example compilation checks applicable to the change;
- inspect the exported API diff and wire/storage compatibility impact;
- move the applicable [CHANGELOG](../CHANGELOG.md) entries from `Unreleased` and document additions, deprecations, default changes, and breaking changes;
- provide migration guidance for every intentional incompatibility;
- identify production validations that remain external to repository tests;
- tag the exact reviewed commit and publish checksums or provenance through the chosen release process.

## Guidance for application authors

Reduce upgrade risk by depending on narrow application-owned ports rather than passing concrete database or transport types through business modules. Keep server assembly in `cmd/...` or an internal platform package, keep message IDs and wire schemas versioned as application contracts, and never import a KPro `internal` package.

If you need a stronger compatibility guarantee before a tagged KPro release exists, vendor or fork a reviewed commit, publish your own change policy, and run compatibility tests against the surface your services actually use.
