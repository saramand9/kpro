# HTTP-to-GORM Write Flow

This guide follows `POST /users` through the HTTP handler, service, and store
until GORM writes the row. HTTP JSON DTOs, domain models, and GORM row models
remain separate throughout the flow.

![HTTP request to GORM flow](assets/gorm-http-request-flow.svg)

## Call Chain

```text
POST /users
-> transport/httpx route
-> internal/modules/users/handler/http.go
-> CreateRequest
-> users/service.CreateInput
-> users.User domain model
-> users/service.UserStore port
-> internal/modules/users/store.userRow
-> database/gorm.Database.GORM()
-> native *gorm.DB.Create
-> MySQL / SQLite / Postgres
-> store.Find reads database-generated fields
-> userResponse
-> HTTP 201 JSON
```

## Layer Responsibilities

| Layer | File | Input | Responsibility | Output |
| --- | --- | --- | --- | --- |
| HTTP route | `transport/httpx` / `server` | Raw HTTP request | Match `POST /users` and create an `*httpx.Context`. | Invoke the users handler. |
| Handler request | `internal/modules/users/handler/http.go` | JSON body | Decode `CreateRequest` and convert it to service input. | `CreateInput{Name, Age}` |
| Service | `internal/modules/users/service/service.go` | `CreateInput` | Trim input, enforce business validation, and create a domain entity. | `users.User{Name, Age}` |
| Store port | `internal/modules/users/service/port.go` | `users.User` | Define the persistence behavior required by the service. | Call a concrete store. |
| GORM store | `internal/modules/users/store` | `users.User` | Map to `userRow`, call GORM, and read back generated ID and timestamps. | Persisted `users.User` |
| GORM resource | `database/gorm/database.go` | `gormstore.Database` | Expose the native `*gorm.DB` and its lifecycle. | `*gorm.DB` |
| Native GORM | `gorm.io/gorm` | GORM-tagged `userRow` | Generate and execute SQL through the configured dialector. | SQL result |
| Database | MySQL / SQLite / Postgres | `INSERT INTO users ...` | Store the row and generate ID and timestamps. | Stored row |
| Handler response | `handler/http.go` | Persisted `users.User` | Map the domain value to `userResponse`. | HTTP 201 JSON |

## Design Rules

- `database/gorm` is a GORM resource and migration integration, not a CRUD
  framework.
- CRUD semantics belong to each module's `store` package, such as
  `internal/modules/users/store`.
- Domain models carry neither `json` nor `gorm` tags. Handler DTOs own the
  HTTP representation; store-local row models own the GORM mapping.
- Store packages apply schema changes through explicit migrations, such as
  `userstore.InitGORM(ctx, db)`.
