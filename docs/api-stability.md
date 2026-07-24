# API Stability, Versioning, and Deprecation

This document defines how KPro communicates compatibility. A pre-1.0 tag is a
reviewed release, but it is not a v1 stability guarantee.

## Current maturity

`v0.1.0` is the initial developer release. Treat the module as pre-1.0
software:

- pin an exact release (or a reviewed commit for unreleased fixes);
- read the changelog and diff before updating that pin;
- expect exported APIs to change while the architecture is refined;
- keep KPro-specific construction and adapter details at your application's platform boundary.

Use an exact version such as
`go get github.com/saramand9/kpro@v0.1.0`; `@latest` is not the
recommended reproducible path during v0. The [Quick Start](quickstart.md)
covers both a release install and checkout-based development.

## Versioning policy

Tagged releases will follow Semantic Versioning:

- `v0.MINOR.PATCH`: a minor release may contain breaking API changes; a patch release should remain source compatible except for urgent security or correctness repairs.
- `v1.MINOR.PATCH` and later: a major version changes for intentional incompatible public API changes, a minor version adds compatible functionality, and a patch version contains compatible fixes.
- Go module major-version path rules apply at `v2` and later.

A tag, release notes, and an upgrade note are the release contract. A branch name or arbitrary commit is not a compatibility promise.

The supported toolchain follows maintained Go release lines. The current
repository baseline is Go 1.25+, and CI verifies Go 1.25.12 and 1.26.5. Dropping
a supported Go line is a compatibility change and must be called out in release
notes.

## Compatibility surface

After `v1`, the compatibility commitment covers documented exported identifiers in packages intended for application use, including `core`, `server`, `transport` (including `transport/transportkit`), `middleware`, `codec`, `observability`, `database`, and `eventing` subpackages.

The following are not stable API:

- anything under an `internal` directory;
- unexported identifiers;
- test helpers, benchmark fixtures, generated temporary artifacts, and example implementation details;
- undocumented behavior that contradicts an exported contract;
- performance characteristics not stated as a measured release guarantee.

Examples and documentation are compatibility signals: CI should continue to compile them, but copying an example's private application structure does not make that structure framework API.

After `v1`, documented `cmd/kpro` subcommands/flags, including `new`, `doctor`,
and `version`, and the versioned
`cmd/benchguard` budget-file schema are supported tooling interfaces. The exact
layout and implementation inside newly generated source may evolve; review and
commit generated files rather than regenerating over an existing service.

Behavioral compatibility matters alongside compilation. Changes to routing
order, cancellation, middleware order, lifecycle ownership, readiness timing,
`ComponentError` provenance, default limits, HTTP/3 0-RTT policy, message
handler-error policy, wire encoding, safe response text, or error classification
must be called out even when function signatures do not change.

`transport/internal/...` remains private. Third-party transports should use the
narrow public contracts in `transport` and helpers in `transport/transportkit`;
the existence or behavior of an internal helper is not a compatibility signal.

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
- run `go mod verify` and pinned `govulncheck` against the complete module, and resolve or explicitly block release on every reachable finding;
- generate the repository coverage profile, enforce the configured aggregate floor, and inspect important package/scenario gaps rather than relying on the total alone;
- inspect the exported API diff and wire/storage compatibility impact;
- move the applicable [CHANGELOG](../CHANGELOG.md) entries from `Unreleased` and document additions, deprecations, default changes, and breaking changes;
- provide migration guidance for every intentional incompatibility;
- identify production validations that remain external to repository tests;
- tag the exact reviewed commit and verify `kpro version` reports the expected tag, commit, build time, platform, and Go version;
- let the tag-triggered release workflow repeat module verification, tests,
  vet, and `govulncheck`; construct a draft with cross-platform archives,
  checksums, and SBOMs; verify its artifact matrix and embedded binary metadata;
  and publish only after those checks pass.

The checked-in workflow and GoReleaser configuration automate draft artifact
construction, verification, and conversion to a published release; they do not
make an arbitrary `v*` tag a supported release. Release notes, upgrade
guidance, provenance review, and the explicit decision to push the release tag
remain required.

## Guidance for application authors

Reduce upgrade risk by depending on narrow application-owned ports rather than passing concrete database or transport types through business modules. Keep server assembly in `cmd/...` or an internal platform package, keep message IDs and wire schemas versioned as application contracts, and never import a KPro `internal` package.

If you need a stronger compatibility guarantee than v0 provides, vendor or
fork the exact release, publish your own change policy, and run compatibility
tests against the surface your services actually use.
