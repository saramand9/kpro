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

`kpro new` has two profiles:

```sh
# Small hand-written HTTP service. This is also the default profile.
kpro new ../orders \
  --module example.com/orders \
  --name orders \
  --profile minimal

# Contract-first HTTP service with a manifest, OpenAPI contract, generated
# bindings, and an application-owned service implementation.
kpro new ../orders-api \
  --module example.com/orders-api \
  --name orders-api \
  --profile http-api
```

Both commands require a missing or empty target directory. When the CLI is run
from a KPro checkout, it records that checkout as a local `replace` in the new
module. `--kpro-replace <checkout>` selects one explicitly. A release-built CLI
pins its release when no replacement is supplied.

The `minimal` profile writes a compact runnable service and no generation
manifest. The `http-api` profile writes:

```text
api/openapi.yaml                 version-controlled API contract
kpro.gen.yaml                    generation manifest
kpro.gen.lock.json               content-addressed generation lock
internal/api/types/              generated transport-neutral types
internal/api/contract/           generated application interface
internal/api/kprohttp/           generated checked HTTP registration
internal/api/service/api.go      create-once application implementation
cmd/<service>/main.go            process assembly
```

The profile runs generation once before it returns. From the generated project:

```sh
go mod tidy
kpro validate
kpro generate --check
go run ./cmd/orders-api
```

If `kpro` is not installed, use the CLI from the pinned KPro checkout in the
same way the repository examples do.

## Manifest

The default manifest is `kpro.gen.yaml`:

```yaml
apiVersion: kpro.dev/codegen/v1alpha1
module: example.com/orders
service: orders
sources:
  - id: public-http
    kind: openapi
    path: api/openapi.yaml
    package: internal/api
  - id: internal-rpc
    kind: proto
    path: api/orders.proto
    package: internal/rpc
  - id: session-messages
    kind: message
    path: api/messages.yaml
    package: internal/messages
```

`module` is the generated application's Go module path. `service` is a Go
identifier used for generated names. Every source has a unique stable `id`, a
supported `kind`, a safe project-relative contract `path`, and a
project-relative Go `package` directory. A path cannot escape the project root,
and one manifest cannot list the same contract path twice.

The manifest is strict: unknown fields, unsupported versions or source kinds,
invalid identifiers, duplicate IDs, and unsafe paths are errors.

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

- `validate` strictly parses and validates the manifest. It does not load every
  contract, invoke `protoc`, or execute a configured plugin.
- `generate` plans every source and then applies the complete virtual file
  plan.
- `generate --dry-run` performs the same planning and safety checks and reports
  proposed creates, updates, removals, and lock changes without writing.
- `generate --check` performs the same work without writing and exits with an
  error if applying the plan would change any output or the lock. Use it in CI.

`--check` and `--dry-run` are mutually exclusive.

The lock is scoped to the manifest path. The default `kpro.gen.yaml` uses
`kpro.gen.lock.json`; `config/public.yaml` uses
`config/public.lock.json`. Both the manifest and its lock must be inside
`--root`. Separate manifests therefore own separate generated-file sets; keep
their output paths disjoint because one lock will treat another lock's output
as unmanaged.

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

Generation rejects absolute and escaping paths, symlink output paths, duplicate
outputs, and generated outputs that collide with unmanaged files. It validates
the complete plan before writing and uses an atomic replacement for each file.
It is not a multi-file filesystem transaction, so keep generation under normal
version-control review.

## OpenAPI HTTP backend

An OpenAPI source uses:

```yaml
- id: public-http
  kind: openapi
  path: api/openapi.yaml
  package: internal/api
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
- document-level or operation-level security requirements.

Every operation needs a unique usable `operationId`. Path-template names and
required path parameters must match exactly. These restrictions avoid a
partially generated or silently mis-bound API.

Authentication remains an application concern even when the source document
declares security schemes. Install fail-closed middleware or enforce access in
an upstream gateway; do not interpret generated route registration as a
security implementation.

## Protobuf and gRPC backend

A protobuf source uses:

```yaml
- id: internal-rpc
  kind: proto
  path: api/orders.proto
  package: internal/rpc
```

Generation requires the standard tools to be installed and available on
`PATH`:

```text
protoc
protoc-gen-go
protoc-gen-go-grpc
```

`kpro doctor` reports each tool as available or `optional missing`; missing
protobuf tools do not fail the diagnostic because other profiles and source
kinds do not need them.

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
KPro's backend unit tests and OpenAPI scaffold smoke test do not require those
external executables.

## KPro message backend

A message source uses:

```yaml
- id: session-messages
  kind: message
  path: api/messages.yaml
  package: internal/messages
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

`dispatch: sync` uses checked synchronous Router registration and preserves the
safer per-session ordering model. `dispatch: async` uses checked asynchronous
registration; select it only when handlers are concurrency-safe and replies may
complete out of order.

The generated adapter strictly rejects unknown JSON fields, trailing JSON
values, and missing/null required fields. A declared response is encoded as
JSON and sent with the request's message ID and sequence number. Omitting
`response` suppresses the generated success reply; normal Router error handling
still applies when the service returns an error.

The adapter targets `core.Router`, so the same generated registration works
with KPro's TCP, KCP, QUIC, UDP, and WebSocket message transports. It does not
generate HTTP, SSE, or gRPC bindings. Nested payload objects, arrays, maps,
enums, alternate encodings, and other schema features are outside this first
contract version.

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
