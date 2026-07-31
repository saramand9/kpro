# Code Generation

KPro's generator turns version-controlled API contracts into checked transport
bindings and create-once application stubs. The workflow is intentionally
contract-first and file-system safe: every source is planned before project
files are changed, output paths stay under the selected project root, and a
content-addressed lock protects generated files from accidental overwrite.

The generator is a pre-1.0 developer feature. Its manifest, message contract,
plugin protocol, generated package layout, and generated Go APIs are currently
`v1alpha1`; they are not a v1 compatibility promise. Pin the KPro CLI to the
same release or reviewed commit as the application and review generated diffs
when upgrading.

Authentication and authorization remain application or gateway
responsibilities. Code generation does not create identities, policy engines,
or access rules. In particular, the OpenAPI backend rejects security
requirements instead of silently publishing an unprotected route.

## Start a project

`kpro new` keeps the compact hand-written `minimal` profile and provides one
unified contract-first `service` profile. Select one or more API models by
repeating `--api`:

```sh
# Small hand-written HTTP service. This remains the default profile.
kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile minimal

# Contract-first HTTP service.
kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile service \
  --api http

# One process exposing HTTP, gRPC, and a KPro Message API over two transports.
kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile service \
  --api http \
  --api grpc \
  --api message \
  --transport tcp \
  --transport websocket
```

The supported API values are `http`, `grpc`, and `message`. The supported
Message transports are `tcp`, `kcp`, `quic`, `udp`, and `websocket`. A Message
API requires at least one explicit `--transport`; transports are invalid
without `--api message`. API and transport flags are repeatable, their order
does not affect the generated layout, and duplicate or unknown values are
errors.

The old `http-api` profile has been removed. Its replacement is
`--profile service --api http`; add further `--api` selections instead of
choosing another protocol-specific profile.

Both profiles require a missing or empty target directory. When publishing into
an existing empty directory, KPro preserves its Unix permission bits, but
replaces the directory itself; platform-specific ACLs and other metadata are
not guaranteed to be retained. When the CLI is run from a KPro checkout, it
records that checkout as a local `replace` in the new module.
`--kpro-replace <checkout>` selects one explicitly. A release-built CLI pins
its release when no replacement is supplied.

The `minimal` profile writes a compact runnable service and no generation
manifest. The `service` profile writes only the contracts and packages selected
by its API flags:

```text
api/http/openapi.yaml                    HTTP contract, when selected
api/grpc/service.proto                   gRPC contract, when selected
api/message/messages.yaml                Message contract, when selected
kpro.service.yaml                        selected APIs and Message transports
kpro.gen.yaml                            generation manifest
kpro.gen.lock.json                       content-addressed generation lock
internal/httpapi/                        HTTP types, contract, adapter, service
internal/grpcapi/                        protobuf, gRPC adapter, service
internal/messageapi/                     Message types, contract, adapter, service
internal/kproapp/endpoints.gen.go         generated selected-endpoint assembly
internal/kproapp/{http,grpc,message}.go   create-once endpoint configuration
cmd/<service>/main.go                    stable process entry point
```

`kpro.service.yaml` is the versioned endpoint-selection input, separate from
the contract-source manifest. For example:

```yaml
apiVersion: kpro.dev/service/v1alpha1
apis:
  - http
  - message
transports:
  - tcp
```

Generation cross-checks these selections against the canonical manifest
sources and renders `internal/kproapp/endpoints.gen.go`. The corresponding
protocol files under `internal/kproapp` are create-once application code, while
`cmd/<service>/main.go` can stay focused on process lifecycle.

An established unified service is not silently treated as a legacy project if
`kpro.service.yaml` disappears. If the generated endpoint assembly or its
create-once endpoint files remain, both `validate` and `generate` fail before
writing and ask for the service configuration to be restored. KPro does not
interpret an accidentally deleted control file as a request to remove endpoint
assembly.

The service profile runs generation once before it returns. A gRPC selection
therefore requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on
`PATH`; the CLI checks them before publishing the target project. From the
generated project:

```sh
go mod tidy
kpro validate
kpro generate --check
go run ./cmd/orders
```

Each selected Message transport owns a separate mounted endpoint and receives
the generated registration independently. QUIC additionally requires
`KPRO_QUIC_CERT_FILE` and `KPRO_QUIC_KEY_FILE` before the generated service can
start; the scaffold does not generate or commit a private key.

If `kpro` is not installed, use the CLI from the pinned KPro checkout in the
same way the repository examples do.

## Add gRPC to a service

A project initially created as an HTTP-only unified service can gain a gRPC
endpoint without regenerating or patching its process entry point:

```sh
# Preview the complete change without writing.
kpro add --api grpc --dry-run

# Apply it in the current project.
kpro add --api grpc

# Or select another project root and manifest.
kpro add --api grpc --root ../orders --config kpro.gen.yaml
```

The current `add` command supports `--api grpc` only. It requires a project
created by the current unified `service` profile, including a valid
`kpro.service.yaml`. The existing generation state must have no pending output
creates, updates, or removals. Normal edits to create-once application
scaffolds are allowed even when only their recorded lock hashes have drifted;
users do not need to run `generate` first to accept those edits. The command
intentionally rejects the removed protocol-specific profile and older projects
that assemble endpoints directly in `cmd/<service>/main.go`; it does not guess
at a migration for application-owned startup code.

Before writing, the command checks `protoc`, `protoc-gen-go`, and
`protoc-gen-go-grpc`, validates the existing service configuration and
manifest, recursively snapshots the project-local import closure of every
built-in protobuf source, and stages the complete desired generation plan
outside the project. Imported proto files are copied into the isolated stage
and included in the compare-and-swap read set; a missing, unsafe, symlinked, or
concurrently changed dependency prevents the addition from committing. It then
commits the following related changes as one compare-and-swap transaction:

- the gRPC selection in `kpro.service.yaml`;
- the `grpc-api` source in the selected generation manifest;
- `api/grpc/service.proto`;
- the generated protobuf, gRPC adapter, and selected-endpoint assembly files;
- the create-once gRPC service and endpoint configuration when absent; and
- the selected manifest's generation lock, including refreshed hashes for
  preserved create-once application files.

A preflight or generation failure, a modified generator-owned file, or a
compare-and-swap conflict is detected before project files are changed. A
failure while installing staged files triggers rollback; when rollback
succeeds, no partial project update remains. In the extreme case that rollback
cannot restore a target, the error reports the exact retained backup path for
manual recovery.

Once all target files have been installed, the project update is successful. If
a superseded backup cannot then be removed, the command reports a warning and
retains that backup instead of misreporting the completed project update as a
failure. Application-owned create-once files are not overwritten; accepted
scaffold hash drift is recorded in the refreshed lock within the same
transaction. `cmd/<service>/main.go` remains byte-for-byte unchanged because it
already delegates endpoint mounting to `internal/kproapp.Mount`.

The command does not run `go mod tidy`; dependency selection remains an
explicit, reviewable application change. After applying the addition, run:

```sh
go mod tidy
go test ./...
kpro generate --check
```

Running the same add again after a successful addition is a byte-preserving
no-op. `--dry-run` performs the same preflight and staging work but makes no
project writes.

## Manifest

The default manifest is `kpro.gen.yaml`:

```yaml
apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders
sources:
  - id: http-api
    kind: openapi
    path: api/http/openapi.yaml
    package: internal/httpapi
  - id: grpc-api
    kind: proto
    path: api/grpc/service.proto
    package: internal/grpcapi
  - id: message-api
    kind: message
    path: api/message/messages.yaml
    package: internal/messageapi
```

`module` is the generated application's Go module path. `service` is a Go
identifier used for generated names. Every source has a unique stable `id`, a
supported `kind`, a safe project-relative contract `path`, and a
project-relative Go `package` directory. A path cannot escape the project root,
and one manifest cannot list the same contract path twice.

The manifest is strict: unknown fields, unsupported versions or source kinds,
invalid identifiers, duplicate IDs, and unsafe paths are errors. Portable path
validation is intentionally stricter than the current host filesystem. Each
path rejects Windows device names and invalid characters, segments ending in a
dot or space, and other non-portable forms. Plans and locks additionally use a
Unicode-normalized, case-folded physical-identity key to reject paths that
could alias after checkout on a supported platform. Invalid UTF-8 paths are
rejected before they can enter a JSON lock. Planned Go files are also checked for
package-name and declaration conflicts across sources, including files whose
package directories differ only by portable casing.

## Commands and lock scope

Use the commands from the project root, or pass the root explicitly:

```sh
kpro validate
kpro generate
kpro generate --dry-run
kpro generate --check

kpro validate --root ../orders --config config/public.yaml
kpro generate --root ../orders --config config/public.yaml --check
```

The commands have distinct purposes:

- `validate` strictly parses and validates the manifest. For a unified service,
  it also validates `kpro.service.yaml` and cross-checks its selections against
  the manifest. It does not load every contract, invoke `protoc`, or execute a
  configured plugin.
- `generate` plans every source and then applies the complete virtual file
  plan.
- `generate --dry-run` performs the same planning and safety checks and reports
  proposed creates, updates, removals, and lock changes without writing.
- `generate --check` performs the same work without writing and exits with an
  error if applying the plan would change any output or the lock. Use it in CI.

`--check` and `--dry-run` are mutually exclusive.

The lock is scoped to the manifest path. The default `kpro.gen.yaml` uses
`kpro.gen.lock.json`; `config/public.yaml` uses
`config/public.yaml.lock.json`. Non-default locks retain the manifest's complete
file name, so `public.yaml` and `public.json` cannot share generation state.
Both the manifest and its lock must be inside `--root`. Separate manifests
therefore own separate generated-file sets; keep their output paths disjoint
because one lock will treat another lock's output as unmanaged.

Older releases named that non-default lock `config/public.lock.json`. Check and
dry-run modes can read an unambiguous legacy lock without changing it; the next
apply migrates it to the complete-filename form with compare-and-swap
protection before applying the planned outputs. If another same-stem manifest
such as `public.json` is present, KPro cannot prove which manifest owns the
legacy lock and fails without writing. Review the generated outputs and move or
remove the legacy lock explicitly in that case.

Commit the contract, manifest, generated outputs, application scaffolds, and
lock together. A review can then distinguish a contract change from its
generated effect, and CI can reproduce that state with `generate --check`.

## Output ownership and write safety

Every planned file has an ownership mode:

| Ownership | First generation | Later generation |
| --- | --- | --- |
| `generated` | Created only if the path is not an unmanaged file | Updated or removed only while its content still matches the previous lock |
| `scaffold` | Created once as an application starting point | Existing edits and deliberate deletion are preserved |
| `user` | Created once | Preserved like `scaffold`; available to plugin authors |

Do not edit files marked `Code generated ... DO NOT EDIT`. If a generated file
does not match its recorded hash, all execution modes fail with a modified-file
error rather than overwriting it. Restore the reviewed generated version or
move the intentional change into the contract, a create-once service file, or
another application-owned file.

When a contract adds a method, an existing create-once service implementation
is not rewritten. The resulting compile error is the explicit prompt to add
the new application method.

Generation rejects absolute and escaping paths, symlink output paths, portable
path aliases, duplicate outputs, non-canonical lock entries, and generated
outputs that collide with unmanaged files. It validates the complete plan
before writing. Apply then stages output replacements and commits output
creates or updates, stale-output removals, and the lock change as one
rollback-capable compare-and-swap batch. Content preconditions are rechecked
before installation. Unchanged planned files, deliberate missing create-once
files, stale missing generated paths, and the lock snapshot remain in the read
set and are checked again after installation but before commit completes. A
later installation or read-set failure attempts to restore all earlier
mutations. If restoration cannot complete, the error identifies every retained
backup that requires manual recovery.

The transaction probes every affected directory for same-filesystem hard-link
support before moving any target. Hard links provide the no-overwrite install
and rollback primitive used by the batch; on a filesystem that does not support
them, apply fails before changing generated targets. Final output permissions
are applied through the installed inode's open handle, so a path replacement is
not accidentally chmodded as if it were the staged output.

Successful installation is not changed into a failure merely because an old
backup cannot be cleaned up; `generate` reports that retained backup as a
warning. This transaction is still not an inter-process filesystem lock or a
crash-atomic commit. Serialize generators for one project, keep generation
under version-control review, and treat process termination, power loss, and
storage failure as conditions that may require repository-based recovery.

## OpenAPI HTTP backend

An OpenAPI source uses:

```yaml
- id: http-api
  kind: openapi
  path: api/http/openapi.yaml
  package: internal/httpapi
```

The current backend deliberately accepts a small OpenAPI 3.0.3 subset and
returns an error for unsupported features before any project output is written.
It generates transport-neutral types, an application interface, checked
`httpx` route registration, and a create-once service implementation.

Supported operations and bindings:

- `GET`, `POST`, `PUT`, `PATCH`, and `DELETE`;
- required path parameters and required or optional query/header parameters;
- inline scalar parameter schemas: `string`, `integer`, `number`, and
  `boolean`;
- `POST`, `PUT`, and `PATCH` with a required `application/json` request body
  referencing a local component schema;
- `DELETE` with no body or the same required JSON component body;
- exactly one `200`, `201`, or `202` JSON response referencing a local
  component schema, or one bodyless `204` response;
- component schemas composed from scalar values, arrays, objects, and local
  component references within the checked subset; required object properties
  are values and optional properties are pointers in generated Go types.

The generated request decoder enforces the non-nullable subset at runtime. A
JSON body equal to `null`, a missing or `null` required property, or an
explicitly `null` optional property is rejected with 400 before application
code runs. Non-null checks recurse through array items, including nested arrays
and referenced component schemas. Required-property presence checks use the
exact JSON property name, so a differently cased name does not satisfy the
contract.

Generated responses are marshaled once and validated against the same
non-nullable subset before any bytes are written. A nil required array, a nil
array item, or another response value that would emit contract-invalid `null`
therefore becomes a server error instead of an invalid success payload.

Generated body handling accepts `application/json` media-type parameters,
returns 415 for another content type, returns 400 for malformed JSON, and lets
KPro's request-size classification map an oversized body to 413.

Not currently supported:

- OpenAPI versions other than exactly 3.0.3;
- `HEAD`, `OPTIONS`, or `TRACE`;
- cookie parameters, parameter references, parameter `content`, or custom
  `style`, `explode`, `allowEmptyValue`, or `allowReserved` serialization;
- a body on `GET`, an optional body, an inline request body schema, or a
  non-JSON request body;
- inline response schemas, multiple or wildcard success responses, or success
  status codes outside 200/201/202/204; error-response schemas are not turned
  into generated Go error types;
- external references, callbacks, enums, nullable schemas, `oneOf`, `anyOf`,
  `allOf`, `not`, `additionalProperties`, or inline nested object schemas;
- schema constraints that the generated Go type cannot enforce, including
  numeric bounds and multiples, string patterns or length bounds, array or
  object size bounds, defaults, uniqueness, read/write annotations, XML, and
  discriminators; `integer` with `format: int64` and `number` with
  `format: double` are the only accepted scalar formats;
- required object-reference cycles that cannot be represented by generated Go
  value fields;
- document-level or operation-level security requirements.

Every operation needs a unique usable `operationId`. Path-template names and
required path parameters must match exactly. Generated method/path patterns are
preflighted with Go's `http.ServeMux` semantics, so equivalent wildcard routes
such as `/users/{id}` and `/users/{name}`, invalid patterns, and other
registration conflicts fail before project output is written. These
restrictions avoid a partially generated or silently mis-bound API. Unsupported
schema constraints are reported explicitly rather than being accepted and then
ignored at runtime.

Schema references must identify exactly one local
`#/components/schemas/<token>` entry; JSON Pointer escapes such as `~1` are
decoded, while nested reference paths are rejected. Property and parameter
names must also be representable exactly by Go's `encoding/json` tag syntax.

Authentication remains an application concern even when the source document
declares security schemes. Install fail-closed middleware or enforce access in
an upstream gateway; do not interpret generated route registration as a
security implementation.

## Protobuf and gRPC backend

A protobuf source uses:

```yaml
- id: grpc-api
  kind: proto
  path: api/grpc/service.proto
  package: internal/grpcapi
```

Generation requires the standard tools to be installed and available on
`PATH`:

```text
protoc
protoc-gen-go
protoc-gen-go-grpc
```

`kpro doctor` reports each tool as available or `optional missing`; missing
protobuf tools do not fail the general diagnostic because HTTP, Message, and
non-protobuf source generation do not need them. They are required when
`kpro new --profile service` selects `--api grpc`; that command checks all
three tools before publishing the new project.

The source must use a `.proto` extension and declare at least one gRPC service.
KPro runs `protoc` in a temporary output directory, collects the standard
`.pb.go` and `_grpc.pb.go` outputs, validates the declared services against the
descriptor set, and then adds:

- a generated checked registration helper targeting
  `grpc.ServiceRegistrar`; and
- a create-once service implementation scaffold.

`transport/grpcx.Server` implements the standard registrar interface, so the
generated application code does not couple the dependency-neutral `server`
package to protobuf or gRPC.

Pin `protoc` and both Go plugin versions in the application's development and
CI environment. `generate --check` invokes them and compares their planned
output, so inconsistent tool versions can produce a legitimate generated diff.
Hermetic backend tests can inject the tool runner, but creating, regenerating,
or checking a real gRPC service project requires the pinned executables.

## KPro message backend

A message source uses:

```yaml
- id: message-api
  kind: message
  path: api/message/messages.yaml
  package: internal/messageapi
```

The contract is strict YAML or JSON:

```yaml
apiVersion: kpro.dev/message/v1alpha1
service: orders
encoding: json
messages:
  - id: 100
    name: createOrder
    dispatch: sync
    request:
      fields:
        - name: orderId
          type: string
          required: true
        - name: quantity
          type: int32
          required: false
    response:
      fields:
        - name: accepted
          type: bool
          required: true

  - id: 101
    name: order-notify
    dispatch: async
    request:
      fields: []
```

`v1alpha1` supports JSON payloads and flat scalar fields of type `string`,
`bool`, `int32`, `int64`, `uint32`, `uint64`, `float32`, `float64`, or `bytes`.
Every field must state `required`; optional generated fields are pointers.
Message IDs must be unique and cannot use `0`, heartbeat `1`, pong `2`, or the
framework error ID `0xffffffff`.

`dispatch: sync` uses checked synchronous registration and preserves the safer
per-session ordering model. `dispatch: async` uses checked asynchronous
registration; select it only when handlers are concurrency-safe and replies may
complete out of order.

The generated adapter strictly rejects unknown JSON fields, trailing JSON
values, and missing/null required fields. Field lookup is case-sensitive even
though Go's default JSON struct matching is not: `ORDERID` is unknown when the
contract declares `orderId`. If a payload contains multiple unknown fields,
their names are sorted and the lexicographically first name is reported, making
the binding diagnostic deterministic. A declared response is encoded as JSON
and sent with the request's message ID and sequence number. Omitting `response`
suppresses the generated success reply; normal Router error handling still
applies when the service returns an error.

The adapter declares a service-specific small interface such as
`OrdersRegistrar`, containing only:

```go
HandleSyncChecked(uint32, core.Handler) error
HandleAsyncChecked(uint32, core.Handler) error
```

Both `*core.Router` and `*server.Endpoint` satisfy that interface. The
recommended server assembly path is to register the generated service directly
on its mounted message endpoint:

```go
endpoint := app.Mount(tcp.New(tcp.Config{
	Addr:  ":8088",
	Codec: codec.KProV1(),
}))

if err := kpromessage.RegisterOrders(endpoint, service); err != nil {
	return err
}
```

Applications that own a `*core.Router` directly can still pass that router to
`RegisterOrders`. The generated adapter is compatible with KPro's TCP, KCP,
QUIC, UDP, and WebSocket message endpoints, but registration is endpoint-local:
register the service separately on every mounted endpoint that should expose
it. The backend does not generate HTTP, SSE, or gRPC bindings. Nested payload
objects, arrays, maps, enums, alternate encodings, and other schema features
are outside this first contract version.

## External generator plugins

A source can replace its built-in backend with an external plugin:

```yaml
sources:
  - id: custom-http
    kind: openapi
    path: api/openapi.yaml
    package: internal/custom
    plugin:
      command: kpro-custom-generator
      args: ["--dialect", "internal"]
```

A plain command name is resolved through `PATH`. A command containing `/` or
`\` is resolved as a safe project-relative regular file. Absolute and escaping
paths are rejected. Arguments are passed directly; KPro never constructs a
command shell.

Plugins are disabled by default:

```sh
kpro validate                         # validates config; never executes plugin
kpro generate                         # fails before plugin execution
kpro generate --allow-plugins         # explicit execution authorization
kpro generate --allow-plugins --check
```

There is intentionally no environment-variable shortcut for authorization.

The plugin receives exactly one JSON request on standard input:

```json
{
  "protocolVersion": "kpro.dev/codegen-plugin/v1alpha1",
  "manifest": {
    "apiVersion": "kpro.dev/codegen/v1alpha1",
    "module": "example.com/orders",
    "service": "orders"
  },
  "source": {
    "id": "custom-http",
    "kind": "openapi",
    "path": "api/openapi.yaml",
    "package": "internal/custom",
    "content": "YmFzZTY0LWVuY29kZWQgc291cmNlIGJ5dGVz"
  }
}
```

It must write exactly one strict JSON response to standard output:

```json
{
  "protocolVersion": "kpro.dev/codegen-plugin/v1alpha1",
  "files": [
    {
      "path": "internal/custom/types.gen.go",
      "content": "cGFja2FnZSBjdXN0b20K",
      "ownership": "generated",
      "mode": 420
    }
  ]
}
```

Byte fields use JSON base64 encoding. `mode` is a Unix permission number such
as decimal `420` for `0644`; zero selects the generator default. File paths,
ownership, modes, duplicates, and the non-empty response are validated through
the same generation plan as built-in backends.

The plugin runs directly in an empty temporary working directory with a
30-second default timeout, an 8 MiB stdout limit, and a 1 MiB stderr limit. It
receives source bytes but not the project root, credentials, runtime config, or
unrelated source metadata through the protocol.

This is protocol and write-path isolation, not a process sandbox. A plugin is
executable code with the current process's operating-system permissions and
environment; it can perform any action those permissions allow. Review and pin
the executable, its arguments, and its provenance before using
`--allow-plugins`, especially in CI.

## Compatibility and upgrade discipline

Until KPro reaches a stable generation contract:

1. Pin the framework and CLI to one exact release or reviewed commit.
2. Use the documentation from that same revision.
3. Pin external protobuf tools and plugin executables.
4. Regenerate in a clean branch and review contract, generated output, and lock
   changes together.
5. Compile and test application-owned service implementations.
6. Run `kpro generate --check` in CI.

An alpha manifest or plugin protocol version is rejected when unsupported; it
is never silently interpreted as another version. Generated APIs and package
layouts may still change before v1, so a successful regeneration is not a
substitute for source review and application tests.
