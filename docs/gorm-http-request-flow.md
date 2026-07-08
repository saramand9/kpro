# HTTP To GORM Request Flow

This document uses `POST /users` to show how an HTTP request becomes a GORM
write without binding HTTP JSON directly to the database model.

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
-> store.Find reads the persisted row
-> userResponse
-> HTTP 201 JSON
```

## Layer Responsibilities

| Layer | File | Input | Responsibility | Output |
| --- | --- | --- | --- | --- |
| HTTP route | `transport/httpx` / `server` | Raw HTTP request | Match `POST /users` and build `*httpx.Context` | User handler call |
| Handler request | `internal/modules/users/handler/http.go` | JSON body | Decode `CreateRequest` and convert it to service input | `CreateInput{Name, Age}` |
| Service | `internal/modules/users/service/service.go` | `CreateInput` | Trim, validate, and create a domain entity | `users.User{Name, Age}` |
| Store port | `internal/modules/users/service/port.go` | `users.User` | Define the persistence behavior the service needs | Concrete store call |
| GORM store | `internal/modules/users/store` | `users.User` | Map domain `User` to `userRow`, call GORM, read back generated fields | Persisted `users.User` |
| GORM resource | `database/gorm/database.go` | `gormstore.Database` | Provide the native `*gorm.DB` and lifecycle hooks | `*gorm.DB` |
| Native GORM | `gorm.io/gorm` | `userRow` with GORM tags | Generate and execute SQL for the selected dialector | SQL result |
| Database | MySQL / SQLite / Postgres | `INSERT INTO users ...` | Persist row and generate ID/timestamps | Stored row |
| Handler response | `handler/http.go` | Persisted `users.User` | Map domain entity to `userResponse` | HTTP 201 JSON |

Key rule: `database/gorm` is not a CRUD framework. It is a lifecycle wrapper
around native GORM. CRUD semantics live in module stores, and HTTP JSON shapes
live in handlers.
