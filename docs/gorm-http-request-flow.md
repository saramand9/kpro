# HTTP 到 GORM 入库流程

这份文档用 `POST /users` 举例，说明一个 HTTP 请求如何经过 handler、service、store，最终通过 GORM 写入数据库。重点是：HTTP JSON、业务模型、GORM row 不直接混用。

![HTTP request to GORM flow](assets/gorm-http-request-flow.svg)

## 调用链

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
-> store.Find 读回数据库生成字段
-> userResponse
-> HTTP 201 JSON
```

## 每层职责

| 层 | 文件 | 输入 | 当前层做什么 | 输出 |
| --- | --- | --- | --- | --- |
| HTTP 路由 | `transport/httpx` / `server` | 原始 HTTP request | 匹配 `POST /users`，构造 `*httpx.Context` | 调用 users handler |
| Handler request | `internal/modules/users/handler/http.go` | JSON body | 解析 `CreateRequest`，转换为 service input | `CreateInput{Name, Age}` |
| Service | `internal/modules/users/service/service.go` | `CreateInput` | trim、业务校验，创建领域实体 | `users.User{Name, Age}` |
| Store port | `internal/modules/users/service/port.go` | `users.User` | 定义 service 需要的持久化能力 | 调用具体 store |
| GORM store | `internal/modules/users/store` | `users.User` | 映射到 `userRow`，调用 GORM，读回自增 ID/时间 | persisted `users.User` |
| GORM resource | `database/gorm/database.go` | `gormstore.Database` | 提供 native `*gorm.DB` 和生命周期能力 | `*gorm.DB` |
| Native GORM | `gorm.io/gorm` | 带 GORM tag 的 `userRow` | 根据 dialector 生成并执行 SQL | SQL result |
| Database | MySQL / SQLite / Postgres | `INSERT INTO users ...` | 写入 row，生成 ID/时间 | stored row |
| Handler response | `handler/http.go` | persisted `users.User` | 映射到 `userResponse` | HTTP 201 JSON |

## 关键规则

- `database/gorm` 不是 CRUD 框架，只负责 GORM 资源生命周期和 migration 工具。
- CRUD 语义放在模块自己的 `store` 包里，例如 `internal/modules/users/store`。
- 领域模型不带 `json` / `gorm` tag；HTTP 输入输出由 handler DTO 负责，GORM tag 由 store-local row model 负责。
- schema 变更通过 store 包里的显式 migration 执行，例如 `userstore.InitGORM(ctx, db)`。