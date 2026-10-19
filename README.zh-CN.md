# KPro

[English](README.md) | 中文

KPro 是一个 pre-1.0 的 Go 多协议应用框架，用同一个服务生命周期组装
HTTP、gRPC 与长连接消息服务。业务可以保留协议原生处理方式，也可以通过
`Message`、`Context` 和 Router 处理消息。

## 适用场景

- 在同一进程组装 HTTP API、gRPC 和 TCP 或 WebSocket 消息服务。
- 为消息服务设置队列、在途请求、出站字节与连接容量限制。
- 统一启动、就绪、分阶段停机和可观测性接入。

已实现 TCP、KCP、QUIC、UDP、WebSocket、HTTP、HTTPS、HTTP/2、HTTP/3、
SSE 和 gRPC，并提供可选 fasthttp 集成。协议实现和仓库测试不构成生产认证
或容量保证。认证、授权由应用或网关负责。

## 版本状态

`v0.1.0` 是初始开发者版本；`v0.2.0` 引入不兼容的组装 API 变更，
最低 Go 版本为 1.26.9。`main` 可能包含后续未发布改动，使用版本时应阅读对应标签的文档。
固定精确版本或经过审查的提交，升级前阅读
[API 稳定性政策](docs/api-stability.md)。生产级 v1 的验收进度见
[路线图](docs/production-v1-roadmap.md)。

## 从源码运行

需要 Go 1.26.9 或更新的兼容工具链，以及可用的 8080 端口。

```sh
git clone https://github.com/saramand9/kpro.git
cd kpro
go run ./examples/quickstart -addr :8080
```

在另一终端验证：

```sh
curl http://localhost:8080/health
```

如果仓库尚未公开，克隆需要相应的 Git 访问权限。
完整路由、请求示例和停机说明见 [快速入门](docs/quickstart.md)。

也可以在 KPro checkout 中生成独立 HTTP 项目：

```sh
go run ./cmd/kpro new ../hello-kpro --module example.com/hello-kpro --name hello-kpro --profile service --api http
cd ../hello-kpro
go mod tidy
go test ./...
go run ./cmd/hello-kpro
```

源码模式会在生成项目中加入指向当前 KPro checkout 的 `replace`。
分发生成项目之前，移除它并固定可拉取的发布版本。
gRPC 生成还需要 protobuf 工具链，详见 [代码生成](docs/code-generation.md)。

## 文档与参与

- [推荐应用结构](docs/golden-path.md) 与 [项目地图](docs/project-map.md)。
- [生产使用边界](docs/production-readiness.md) 与 [运维和容量规划](docs/operations.md)。
- [贡献指南](CONTRIBUTING.md)、[更新记录](CHANGELOG.md) 与 [开源发布准备](docs/open-source-release.md)。
- 漏洞请按 [安全政策](SECURITY.md) 私密报告。

欢迎可复现的缺陷报告、API 使用反馈和附带测试条件的性能结果。
KPro 使用 [Apache License 2.0](LICENSE)。
