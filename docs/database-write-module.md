# Database Write Module

KPro provides optional application-building helpers for database-backed services.
The default path is lightweight and based on `database/sql`; GORM is available as
an optional ORM handle when applications need richer model/query behavior.

The recommended application shape is:

```text
protocol route -> request DTO -> Entity[T] -> CRUDService[T] -> dbx.Store[T] -> DB / GORM
```

Handlers adapt TCP, HTTP, gRPC, or another protocol into validated request DTOs
and then map those DTOs to entity inputs. `Entity[T]` is the model-centric entry
point for common CRUD. `dbx.Store[T]` owns data access. `dbx.Database` owns
connection-pool setup, transactions, optional async write workers, and optional
GORM integration.

## Opening A Database

Plain `database/sql`:

```go
database, err := dbx.Open(ctx, dbx.Config{
	DriverName:   "sqlite",
	DSN:          "file:app.db",
	Dialect:      dbx.SQLite,
	MaxOpenConns: 10,
})
```

GORM-backed connection pool:

```go
database, err := dbx.Open(ctx, dbx.Config{
	GORMDialector: sqlite.Open("file:app.db"),
	Dialect:       dbx.SQLite,
	MaxOpenConns:  10,
})
```

Register the database with the server lifecycle:

```go
app := server.New()
app.Manage(database)
```

`Server.Shutdown` drains async writes and closes the underlying pool.

## Entity CRUD

For common create/read/update/delete resources, define the storage model and
create an `appx.Entity[T]`:

```go
type User struct {
	ID   int64  `json:"id" db:"id,pk,auto"`
	Name string `json:"name" db:"name"`
	Age  int    `json:"age" db:"age"`
}

users := appx.NewEntity[User](database, appx.EntityConfig{Table: "users"})
```

Business HTTP handlers should decode request-specific DTOs, validate them, and
then call the entity:

```go
type CreateUserRequest struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func (r CreateUserRequest) ToUser() User {
	return User{Name: strings.TrimSpace(r.Name), Age: r.Age}
}

endpoint := app.HTTP(":8080")
endpoint.Post("/users", func(ctx *httpx.Context) error {
	var req CreateUserRequest
	if err := ctx.JSONBody(&req); err != nil {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "invalid json"})
	}
	if strings.TrimSpace(req.Name) == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "name is required"})
	}
	user := req.ToUser()
	created, err := users.Create(ctx.Context(), &user)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, created)
})
```

`appx.BindHTTPEntity` remains available for simple internal CRUD surfaces where
HTTP JSON intentionally matches the storage model exactly. It registers:

```text
GET    /users       list rows
GET    /users?id=1  find one row
POST   /users       create one row from JSON
PUT    /users       update one row from JSON
DELETE /users?id=1  delete one row
```

Use explicit DTO handlers when the HTTP shape differs from the storage model,
or when the route needs business rules such as password hashing, order state
transitions, permissions, or cross-table transactions.

## Store Access

`dbx.Store[T]` is available when custom code needs direct data access:

```go
store := dbx.NewStore[User](database, "users")
_, err = store.Create(ctx, &User{Name: "Ada", Age: 37})
_, err = store.Update(ctx, &User{ID: 1, Name: "Ada", Age: 38})
_, err = store.Upsert(ctx, &User{ID: 1, Name: "Ada", Age: 39}, dbx.Conflict("id"))
```

For non-critical writes, use the bounded async view:

```go
future, err := store.Async().Create(ctx, &user)
_, err = future.Wait(ctx)
```

If the async queue is full, submit returns `dbx.ErrAsyncQueueFull`; work is not
silently dropped.

## GORM Escape Hatch

When `GORMDialector` or `GORMDB` is configured, applications can use native GORM:

```go
gormDB := database.GORM()
err := gormDB.AutoMigrate(&User{})
err = dbx.NewStore[User](database, "users").GORM().Create(ctx, &user)
```

KPro does not force GORM on every store. Simple writes can use `appx.Entity[T]`
or `dbx.Store[T]`; complex ORM behavior can use `database.GORM()` or
`store.GORM()`.

## Module Registration

`appx` provides lightweight module registration contracts:

```go
type UsersModule struct {
	users *users.Service
}

func (m UsersModule) RegisterHTTP(e *httpx.Endpoint) {
	users.RegisterHTTP(e, m.users)
}

appx.RegisterHTTP(app.HTTP(":8080"), usersModule)
```

The runnable example at `examples/dbx-http/server` shows:

```text
HTTP DTO handler -> users.Service -> Entity[User] -> CRUDService[User] -> dbx.Store[User] -> DB/GORM
```

## Raw SQL

Generated writes are for common operations. For complex reads or
provider-specific SQL, keep using `database.DB()` or `Writer.ExecContext`.
