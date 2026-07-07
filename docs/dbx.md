# DBX Database Module

KPro exposes lightweight SQL helpers through the `dbx` package. The module uses
`database/sql` plus dbx-generated SQL for common writes and simple reads.

For new multi-database design work, treat `dbx` as the SQL implementation
under `database/sql`. The higher-level fact/outbox/projection direction is
described in [database-architecture.md](database-architecture.md).

SQLite, MySQL, and Postgres are SQL dialects used by dbx for placeholders,
identifier quoting, and generated upsert syntax.

The recommended application shape is:

```text
protocol route -> handler/controller DTO -> service -> dbx.Store[T] -> SQL DB
```

Handlers adapt TCP, HTTP, UDP messages, gRPC, or another protocol into validated
request DTOs and then map those DTOs to service inputs. Services own business
rules and call module-specific store interfaces backed by `dbx.Store[T]` or
explicit SQL APIs for persistence. `dbx.Database` owns connection-pool setup,
transactions, and async write workers.

## Opening a Database

Plain `database/sql` path:

```go
database, err := dbx.Open(ctx, dbx.Config{
	DriverName:   "sqlite",
	DSN:          "file:app.db",
	Dialect:      dbx.SQLite,
	MaxOpenConns: 10,
})
```

If `Dialect` is omitted, dbx infers it from `DriverName` when possible.

Register the database with the server lifecycle:

```go
app := server.New()
app.Manage(database)
```

`Server.Shutdown` calls `Database.Shutdown`, which drains async writes and closes
the underlying `database/sql` pool.

## Migrations

Use `database/sql.NewMigrator` for source-of-truth schema setup. Each migration
has a stable ID and is recorded in `schema_migrations` with a checksum and
status:

```go
migrator := sqlstore.NewMigrator(database, "")
err := migrator.Apply(ctx, sqlstore.Migration{
	ID: "users_001_create",
	Statements: []string{`CREATE TABLE users (...)`},
})
```

Runnable examples expose module schema as migrations, then call module `Init`
during startup.

Never change the statements for an already-applied migration ID. The migrator
will reject the change as a checksum mismatch. Failed migrations are recorded and
must be inspected before retrying.

## Store[T]

For common create/read/update/delete resources, define a storage model and create
a typed store:

```go
type User struct {
	ID   int64  `json:"id" db:"id,pk,auto"`
	Name string `json:"name" db:"name"`
	Age  int    `json:"age" db:"age"`
}

users := dbx.NewStore[User](database, "users")
```

`Store[T]` provides common persistence operations:

```go
_, err = users.Create(ctx, &User{Name: "Ada", Age: 37})
_, err = users.Update(ctx, &User{ID: 1, Name: "Ada", Age: 38})
_, err = users.Upsert(ctx, &User{ID: 1, Name: "Ada", Age: 39}, dbx.Conflict("id"))
_, err = users.BatchCreate(ctx, []User{{Name: "Grace"}, {Name: "Linus"}})

user, err := users.Find(ctx, 1)
list, err := users.List(ctx)
_, err = users.Delete(ctx, 1)
```

Store operations use `database/sql` and dbx-generated SQL based on `db` struct
tags. GORM is not hidden inside `dbx.Store[T]`; use the peer `database/gorm`
adapter when a module should be backed by GORM.

Business handlers should decode request-specific DTOs and call a service. The
service owns validation and depends on a business-shaped persistence port. A
module store adapter hides `dbx.Store[T]` and any `sql.Result` details:

```go
type CreateUserRequest struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type UserService struct {
	users UserStore
}

func (s *UserService) Create(ctx context.Context, name string, age int) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("name is required")
	}
	user := User{Name: name, Age: age}
	return s.users.Create(ctx, user)
}

userService := &UserService{users: userstore.NewDBX(database)}
endpoint := app.HTTP(":8080")
endpoint.Post("/users", func(ctx *httpx.Context) error {
	var req CreateUserRequest
	if err := ctx.JSONBody(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json"})
	}
	created, err := userService.Create(ctx.Context(), req.Name, req.Age)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return ctx.JSON(http.StatusCreated, created)
})
```

Keep generic framework stores inside module store adapters or the application
composition layer. Services should speak in business values and errors, not
database framework return types.

## AsyncStore

For non-critical writes, use the bounded async store view. Async store operations
call the same `Store[T]` path:

```go
future, err := users.Async().Create(ctx, &user)
if err != nil {
	return err
}
_, err = future.Wait(ctx)
```

If the queue is full, submit returns `dbx.ErrAsyncQueueFull`; work is not
silently dropped. After shutdown, async submission returns `dbx.ErrAsyncClosed`.

`Database.Async()` also exposes lower-level generated write helpers:

```go
future, err := database.Async().Insert(ctx, "users", User{Name: "Ada", Age: 37})
```

## Transactions

`Database.Tx` opens a `database/sql` transaction and commits when the callback
returns nil. Returning an error rolls the transaction back.

```go
err := database.Tx(ctx, func(tx *dbx.Tx) error {
	if _, err := tx.Writer().Insert(ctx, "users", User{Name: "Ada", Age: 37}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE audit SET count = count + 1 WHERE name = ?", "users")
	return err
})
```

The transaction wrapper currently exposes `SQLTx()`, `Writer()`, and
`ExecContext()`. Use those APIs for transaction-scoped generated writes or raw
SQL. `dbx.Store[T]` also honors `dbx.WithTx(ctx, tx)`, and
`database/sql.UnitOfWork` installs that context automatically for repositories
called inside the unit of work.

## GORM As Peer Storage

GORM is a framework-level storage implementation, not a dbx execution branch.
Use `database/gorm` when a module should be backed by GORM:

```go
gormResource, err := gormstore.Open(ctx, gormstore.Config{
	Name:      "main",
	Dialector: sqlite.Open("file:app.db"),
})
stores := NewGORMStores(gormResource)
RegisterHTTPModules(endpoint, stores)
```

The business service still depends on its own store interface, so startup code
can choose either `dbx.NewStore[User](database, "users")` or
`gormstore.NewStore[User](gormResource, "users")` explicitly.

## Application Routing

Keep protocol registration in the application routing layer. The dbx example does
this in `examples/dbx-http/server/internal/app/routes.go` by creating composed
stores once, then registering protocol modules from those ports:

```go
if err := InitDBXSchemas(ctx, database); err != nil {
	return err
}

httpEndpoint := app.HTTP(config.HTTPAddr)
if err := RegisterHTTPModules(httpEndpoint, NewDBXStores(database)); err != nil {
	return err
}
MountOptionalProtocolModules(app, config)
```

The runnable example at `examples/dbx-http/server` uses application modules under
`internal/modules/users`, `internal/modules/projects`, `internal/modules/login`,
and `internal/modules/reports`, and shows:

```text
HTTP handler/controller -> users/projects service -> dbx.Store[T] -> SQL DB
UDP message handler     -> login service
gRPC unary handler      -> reports service
```

## UDP Message Access

The dbx example mounts an independent `login` business module on the UDP message
endpoint. The UDP handler decodes KPro message payloads as JSON DTOs, calls the
login service, and returns JSON in the response message payload:

```text
UDP packet -> KPro message -> login UDP handler -> login service
```

The example registers this login message id:

```text
3001  login  payload: {"username":"admin","password":"secret"}
```

HTTP and UDP intentionally use different modules in this example: HTTP exposes
`users` and `projects`, while UDP exposes `login`. This keeps protocol adapters
separate from business logic and shows that every protocol can mount its own
business surface.

## gRPC Report Access

The dbx example mounts a separate `reports` business module on gRPC. The handler
uses protobuf `structpb.Struct` as the DTO shape so the example can run without a
proto generation step:

```text
gRPC /kpro.reports.v1.ReportService/BuildSummary
request:  {"project":"apollo","owner":"Ada"}
response: {"project":"apollo","owner":"Ada","title":"apollo summary","status":"ready","items":2}
```

The protocol adapter remains separate from business logic:

```text
gRPC unary request -> reports gRPC handler -> reports service
UDP message        -> login UDP handler    -> login service
```

## Raw SQL

Generated SQL is intended for common operations. For complex reads,
provider-specific SQL, or transaction-scoped custom statements, use
`database.DB()`, `database.Writer().ExecContext`, or `tx.ExecContext`.

## Production Testing

The default test suite uses SQLite and in-memory fakes. The `dbintegration` tag
adds SQL backend tests for Store, Migrator, transaction, and Outbox behavior.
Set `KPRO_TEST_MYSQL_DSN` and `KPRO_TEST_POSTGRES_DSN` to include MySQL and
Postgres. See [database-production.md](database-production.md) for the full
operational contract.
