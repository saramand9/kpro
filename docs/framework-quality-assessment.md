# Framework Quality Assessment

This document records a repeatable technical quality assessment for KPro. It
is an evidence-based engineering baseline, not a production certification, an
availability guarantee, or a measure of project popularity.

## Current Baseline

| Field | Value |
| --- | --- |
| Assessment date | 2026-07-28 |
| Runtime baseline | `f082c4b` (`fix(runtime): bound I/O and expose shutdown errors`) |
| Release posture | Post-`v0.1.0`, pre-1.0 unreleased baseline |
| Validation toolchain | Go 1.26.5 on Windows |
| Equal-weight technical score | **8.2 / 10** |
| Qualitative basic-application fit, up to about 1,000 users | **8.6 / 10** |
| Compatibility and release maturity | **7.2 / 10** |

The scenario-fit rating is a qualitative suitability judgment, not a capacity
calculation, and it does not contribute to the technical average. It assumes a
typical bounded API or CRUD application, measured workload staying within
configured per-instance limits, application-owned persistence and identity,
and no framework-level hard SLA, multi-region, or extreme fan-out requirement.
The user count alone does not imply concurrency, QPS, payload size, or protocol
mix; those still require workload-specific validation. A modest user count
reduces the importance of extreme-scale evidence, but it does not turn missing
release history or production evidence into technical maturity.

Authentication, authorization, identity management, and policy administration
are intentionally outside this assessment. They may be supplied by a separate
application or gateway and should be assessed with that system.

## Scoring Method

Scores use the following interpretation:

| Score | Meaning |
| ---: | --- |
| Below 6.0 | Important engineering gaps prevent dependable use in the stated scope. |
| 6.0 to 6.9 | Basically usable, with material limitations or manual safeguards. |
| 7.0 to 7.9 | Good engineering quality and suitable for real development. |
| 8.0 to 8.9 | Production candidate with strong repository evidence. |
| 9.0 to 10.0 | Exceptional evidence within the assessed dimension. |

Dimension scores describe the evidence within that dimension. An overall score
of 9 or higher additionally requires cross-dimension evidence of stable use
across teams, workloads, and releases.

The equal-weight score is the arithmetic mean of the dimensions below. It is
rounded to one decimal place: `107.2 / 13 = 8.2`.

Correctness, security, and lifecycle behavior are gates. Lifecycle is evaluated
through the correctness-and-reliability and observability-and-operability
dimensions. An unresolved release-blocking defect in any gate caps the overall
score at 5.9 regardless of the arithmetic mean. A release-blocking defect is one
that can cause data loss, unauthorized access, process hang or unbounded
shutdown, or sustained false readiness under documented defaults without a
reliable mitigation. Lower-severity defects reduce the affected dimension but
do not automatically activate the cap. Feature count or microbenchmark speed
cannot compensate for a failed gate.

## Dimension Scores

| Dimension | Score | Evidence | Main reason it is not higher |
| --- | ---: | --- | --- |
| Positioning and applicability | 8.0 | The README, Golden Path, and project map define a multi-transport application framework and its non-goals. | Network, data, and eventing coverage make the total scope broad. |
| Architecture quality | 8.5 | `server` is independent of concrete transports; message and native protocols have explicit contracts, lifecycle ownership, and dependency-boundary tests. | Supporting many protocol models still creates internal lifecycle and configuration complexity. |
| Correctness and reliability | 8.4 | Bounded admission, cancellation, timeout, request limits, shutdown error propagation, readiness state, full tests, and race tests are present. | There is no durable evidence from long-running production, fault injection, or incident recovery. |
| API and usability | 8.0 | Checked constructors, unified middleware, explicit mounting, safe defaults, examples, and `kpro new` provide a usable path. | Multiple mount modes and the broad configuration surface increase learning cost; the API remains pre-1.0. |
| Functional completeness | 8.7 | Routing, middleware, message and native transports, database adapters, event delivery, health, metrics, tracing, logging, and scaffolding form a complete base-application path. | Some application concerns are deliberately external, and breadth does not prove equal depth in every adapter. |
| Extensibility | 8.6 | Public transport contracts, `transportkit`, middleware, codecs, native runners, descriptors, and validation allow extension without importing transport internals. | Extension authors must understand both message and native invocation models. |
| Performance and capacity | 8.0 | Hot-path benchmarks have repository-owned budgets; routing and HTTP allocation costs are guarded; capacity limits and overload behavior are explicit. | Microbenchmarks are not workload capacity evidence; there is no committed load curve, soak result, or hardware matrix. |
| Security | 8.2 | TLS controls, payload and connection limits, safe error mapping, dependency verification, pinned vulnerability scanning, and patched reachable dependencies are in place. | There is no independent security audit or repository threat model, and deployment policy remains application-owned. |
| Observability and operability | 8.5 | Health, readiness, metrics, tracing, structured logging, component errors, bounded shutdown, runbooks, and capacity guidance are documented. | Alert rules, dashboards, backend policy, and real incident evidence remain external. |
| Testing and engineering quality | 9.0 | CI covers unit and integration tests, race detection, vet, coverage, vulnerability scanning, benchmarks, scaffold smoke tests, databases, Windows, and release checks. | Protocol interoperability, chaos testing, and long-duration scenarios can still expand. |
| Documentation and developer experience | 8.8 | Quick Start, Golden Path, project map, analysis index, examples, operations, production checklist, and extension guidance are connected. | The documentation set is large enough that task-oriented navigation must remain actively maintained. |
| Compatibility and release maturity | 7.2 | SemVer policy, changelog, release automation, checksums, SBOM generation, and upgrade guidance exist. | `v0.1.0` has no multi-release compatibility history or externally observed upgrade cycle. |
| Maintainability | 7.3 | Package ownership, dependency rules, evidence indexes, tests, and optional integrations reduce change risk. | The dependency graph and the number of protocols, adapters, and examples create a substantial long-term ownership burden. |

## Verification Evidence

The baseline was accepted only after the following checks passed with Go
1.26.5. The durable command definitions live in the
[CI workflow](../.github/workflows/ci.yml):

- `go test -p 1 ./... -count=1 -timeout=15m`;
- `go test -race -count=1 -timeout=20m ./...`;
- `go vet ./...`;
- `go mod verify` and `go mod tidy -diff`;
- pinned `govulncheck`, reporting zero vulnerabilities on reachable call paths;
- the CI coverage command, producing 75.8% aggregate statement coverage
  against a 55% floor;
- the hot-path benchmark guard in `cmd/benchguard`;
- generated-service scaffold compilation;
- tests and vet for `benchmark/compare`.

The 75.8% coverage value and zero-reachable-vulnerability result are observations
from the assessment run; their raw local output is not committed. The CI
workflow preserves the coverage floor and scanning recipe, not those exact
results. Reassessors must rerun both checks instead of treating the recorded
values as immutable evidence.

These results establish repository behavior on the assessed toolchain. They do
not establish service capacity, latency objectives, database topology support,
or deployment-specific availability.

## Interpretation

At this baseline KPro is more than an experimental networking library. It is a
high-quality production candidate for internal and small-to-medium application
development, especially when the expected user population is below roughly
1,000 and the application configures explicit limits, persistence, identity,
and infrastructure policy.

The project is not rated above 9 because that level requires evidence which
cannot be created by adding repository code alone:

- multiple real applications operating over time;
- measured load, soak, degradation, and recovery results;
- several backward-compatible release and migration cycles;
- incident feedback from deployed systems;
- independent developer and security review.

The largest remaining quality gain therefore comes from production evidence
and controlled simplification, not from adding more protocol or adapter count.

## Evidence Gaps To Close

1. Record a representative load and soak test with workload assumptions,
   hardware, limits, latency percentiles, memory, and shutdown behavior.
2. Exercise dependency failures and protocol drains through repeatable fault
   injection, including slow peers and unavailable data stores.
3. Validate two or more independently structured applications and record where
   the public API creates avoidable setup or debugging cost.
4. Complete at least one minor-version upgrade cycle using the compatibility
   and migration policy.
5. Review dependency and adapter breadth periodically and split or remove
   surfaces whose ownership cost exceeds their demonstrated use.
6. Perform a threat-model review with the actual gateway, identity system,
   deployment topology, and data classification in scope.

## Reassessment Rules

Reassess after a minor release, a lifecycle or dependency-boundary change, a
reachable security finding, a public API migration, or new production evidence.

For every reassessment:

1. record the date, commit, release, operating system, and Go toolchain;
2. run the checks in [Analysis Index](analysis-index.md);
3. change a score only when code, tests, documentation, or external evidence
   changed;
4. keep scenario fit separate from the equal-weight technical score;
5. record newly discovered gate defects before averaging scores;
6. add the new baseline to the history table instead of silently replacing the
   previous result.

## Assessment History

| Date | Commit | Technical score | Basic-application fit | Summary |
| --- | --- | ---: | ---: | --- |
| 2026-07-28 | `f082c4b` | 8.2 | 8.6 | Runtime limits and shutdown error handling verified; first durable quality baseline. |

Related evidence and boundaries are maintained in
[Project Map](project-map.md), [Production Readiness](production-readiness.md),
[Operations](operations.md), [API Stability](api-stability.md), and
[Dependency Boundaries](dependency-boundaries.md).
