# DBX Database Module

KPro exposes database helpers through the `dbx` package. The module supports two
integration paths:

- `database/sql` plus dbx-generated SQL for common writes and simple reads.
- GORM as an adapter path, configured with `GORMDialector` or `GORMDB`.

SQLite, MySQL, and Postgres are SQL dialects used by dbx for placeholders,
identifier quoting, and generated upsert syntax. GORM is an integration path, not
a database type. A GORM-backed SQLite connection, for example, still uses the
SQLite dialect.

The recommended application shape is:

```text
protocol route -> handler/controller DTO -> service -> dbx.Store[T] -> DB / GORM
```

Handlers adapt TCP, HTTP, UDP messages, gRPC, or another protocol into validated
request DTOs and then map those DTOs to service inputs. Services own business
rules and call `dbx.Store[T]` or explicit SQL/GORM APIs for persistence.
`dbx.Database` owns connection-pool setup, transactions, async write workers, and
optional GORM access.

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

GORM adapter path:

```go
database, err := dbx.Open(ctx, dbx.Config{
	GORMDialector: sqlite.Open("file:app.db"),
	Dialect:       dbx.SQLite,
	MaxOpenConns:  10,
})
```

`GORMDB` can also be used to reuse an already opened `*gorm.DB`. Use either
`GORMDialector` or `GORMDB`, not both. If `Dialect` is omitted, dbx infers it
from the `DriverName` or GORM dialector name when possible.

Register the database with the server lifecycle:

```go
app := server.New()
app.Manage(database)
```

`Server.Shutdown` calls `Database.Shutdown`, which drains async writes and closes
the underlying `database/sql` pool.

## Store[T]

For common create/read/update/delete resources, define a storage model and create
a typed store:

```go
type User struct {
	ID   int64  `json:"id" db:"id,pk,auto" gorm:"column:id;primaryKey;autoIncrement"`
	Name string `json:"name" db:"name" gorm:"column:name"`
	Age  int    `json:"age" db:"age" gorm:"column:age"`
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

When the database was opened with GORM, store operations use GORM internally for
the typed model and configured table. Otherwise they use `database/sql` and
dbx-generated SQL based on `db` struct tags.

Business handlers should decode request-specific DTOs and call a service. The
service owns validation and uses `dbx.Store[T]` for storage:

```go
type CreateUserRequest struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type UserService struct {
	users *dbx.Store[User]
}

func (s *UserService) Create(ctx context.Context, name string, age int) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("name is required")
	}
	user := User{Name: name, Age: age}
	_, err := s.users.Create(ctx, &user)
	return user, err
}

userService := &UserService{users: users}
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

Keep generic store access inside the service layer, where business validation,
authorization, transactions, and cross-model behavior can be expressed clearly.

## AsyncStore

For non-critical writes, use the bounded async store view. Async store operations
call the same `Store[T]` path, so they also use GORM when GORM is configured:

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
SQL.

## Native GORM Access

When `GORMDialector` or `GORMDB` is configured, applications can still use native
GORM for ORM behavior outside the common store path:

```go
gormDB := database.GORM()
err := gormDB.AutoMigrate(&User{})
err = dbx.NewStore[User](database, "users").GORM().Create(ctx, &user)
```

KPro keeps GORM behind `dbx.Store[T]` for common CRUD. More complex ORM behavior
can use `database.GORM()` or `store.GORM()` directly.

## Application Routing

Keep protocol registration in the application routing layer. The dbx example does
this in `examples/dbx-http/server/internal/app/routes.go` by constructing
services and registering each protocol handler explicitly:

```go
userService := userservice.New(database)
projectService := projectservice.New(database)

httpEndpoint := app.HTTP(config.HTTPAddr)
userhandler.RegisterHTTP(httpEndpoint, userService)
projecthandler.RegisterHTTP(httpEndpoint, projectService)

if config.UDPAddr != "" {
	udpEndpoint := app.UDP(config.UDPAddr, udp.Config{
		Codec:      codec.KProV1(),
		MaxPacketSize: 2048,
	}).Use(middleware.Recover(), middleware.MaxPayload(1500))
	loginhandler.RegisterUDP(udpEndpoint, loginservice.New(loginservice.Config{}))
}

if config.GRPCAddr != "" {
	grpcEndpoint := app.GRPC(config.GRPCAddr).Use(middleware.Recover())
	reporthandler.RegisterGRPC(grpcEndpoint, reportservice.New(reportservice.Config{}))
}
```

The runnable example at `examples/dbx-http/server` uses application modules under
`internal/modules/users`, `internal/modules/projects`, `internal/modules/login`,
and `internal/modules/reports`, and shows:

```text
HTTP handler/controller -> users/projects service -> dbx.Store[T] -> DB/GORM
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
`database.DB()`, `database.Writer().ExecContext`, `tx.ExecContext`, or native
GORM when configured.

