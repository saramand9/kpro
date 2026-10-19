# KPro 开源发布准备

KPro 以 pre-1.0 开发者版本开放试用。本页记录 `v0.2.0` 的发布准备与验证，
该 minor 包含相对 `v0.1.0` 不兼容的 API 变更。公开标签和检查通过不表示
生产级 v1 验收通过；发布产物状态见文末的 Release 工作流。

## 公开仓库之前

- [ ] 维护者确认代码和素材的公开分发权利，检查第三方代码的许可证及归属声明。
- [ ] 完整 Git 历史、所有待公开分支和标签通过凭据扫描，人工复核扫描范围及例外。
- [ ] 检查远程 Actions 日志、附件、Issue 和 PR 中的私人信息。
- [ ] 曾暴露的有效凭据完成撤销或轮换，再处理历史记录。
- [ ] 确认所有将推送的文件均属于发布范围；本地私密文件和历史重写目录不打包。

仓库已有 Apache-2.0 `LICENSE`。本地忽略规则只是防止误提交，不能代替历史扫描。
切换为公开后 Actions 历史和日志也会公开，参见
[GitHub 仓库可见性说明](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/setting-repository-visibility)。

## 发布验证

在干净检出记录提交、操作系统、Go 与 C 编译器版本、完整命令、退出码和日志。
使用 [CI](../.github/workflows/ci.yml)、
[安全工作流](../.github/workflows/security.yml) 和
[API 发布清单](api-stability.md#release-checklist) 作为检查入口。

- [x] 全量测试、核心 race、TCP 确定性故障注入、vet、格式检查通过。
- [x] `go mod verify`、`go mod tidy -diff`、无本地 replace/exclude 检查通过。
- [x] 纯 Go 编译、漏洞检查、代码安全扫描、生成检查及性能预算通过。
- [x] 根模块与 `benchmark/compare` 分别验证。
- [x] Linux CI、支持的 Go 版本与真实数据库集成检查通过，并记录 CI 链接。
- [x] 路线图中的 TCP 批写契约修正具有回归证据；性能影响仍须按路线图验证。
- [x] 对未执行或失败项明确记录，不能使用历史通过结果替代。

## 外部用户入门

- [x] 用仓库外的新目录跑通 HTTP scaffold、生成检查、编译及实际请求。
- [x] 公开标签后在无本地 replace 的独立目录验证精确版本的 `go get` 和 CLI 安装。
- [x] README、快速入门、版本说明和模块版本一致。
- [ ] 邀请外部开发者独立执行入门流程，修复阻碍使用的问题。

源码模式生成的项目会引用本地 KPro；它只能验证当前 checkout。
发布后还须验证模块代理和版本化安装路径，参见
[Go 模块发布流程](https://go.dev/doc/modules/publishing)。

## GitHub 设置与发布

- [x] 核实仓库所有者、远程地址、默认分支及可见性。
- [x] 启用默认分支保护与实际存在的 CI 必需检查。
- [x] 启用私密漏洞报告，验证 `SECURITY.md` 的入口可用。
- [x] 设置简介、Topics 和基本 Issue 标签；文档入口在 README。
- [x] 确认安全维护范围；下一 minor 发布时同步更新 `SECURITY.md`。
- [x] 完成并复核 `v0.2.0` 发布说明，选定通过检查的提交。
- [x] 创建 annotated tag 并触发现有 release 工作流，验证归档、校验和及 SBOM。
- [ ] 验证发布后的安装和示例，再发布介绍文章。

现有 release 工作流会在检查和产物验证后自动公开 Release；推送标签属于发布动作。
不要在验证未完成时提前推送标签。

## 本次本地准备记录

起点：`8058c9d`；日期：2026-10-19；Windows amd64；Go 1.26.4；存在 GCC。
工作区验证不等于发布提交的干净检出验收。

- 已有本地 `v0.1.0` 标签，远程标签和 Release 状态待核实。
- GitHub CLI 读取仓库信息返回 HTTP 401，远程设置及 Actions 结果待核实。
- 已补 PR 模板和 `v0.2.0` 发布说明草稿。
- 已对 TCP 帧上限建立失败回归并修正实现；完整验证结果见后续记录。

验证记录（工作区，未提交）：

| 检查 | 结果 |
| --- | --- |
| Gitleaks v8.30.1，`git . --config .gitleaks.toml --log-opts=--all --redact=100 --no-banner` | 244 个提交，未发现未豁免记录；白名单仅限指定 WebSocket 测试文件中的固定 nonce，包括解码值 |
| `go mod verify` | 通过 |
| `go mod tidy -diff` | 无差异 |
| `CGO_ENABLED=0 go vet -p 1 ./...` | 通过 |
| `CGO_ENABLED=0 GOMAXPROCS=4 go test -p 1 ./... -count=1 -timeout=10m` | 未通过：仅 SSE loadgen 进程以 Windows `0xc0000005` 退出；其余包通过，包括跨包集成测试 |
| `CGO_ENABLED=0 go test -p 1 ./benchmark/cmd/sse-loadgen -count=1 -timeout=2m` | 单包复测通过；未定位全量运行中的异常原因 |
| `go test ./transport/tcp -run '^TestSessionBatchWriter' -count=1` | 通过 |
| `go test -p 1 -race ./transport/tcp -run '^Test(SessionBatchWriter\|TCPFault)' -count=1 -timeout=3m` | 通过 |
| `GOMAXPROCS=4 go test -p 1 -race ./core ./server ./transport/... ./observability/... ./eventing/... ./cmd/kpro ./cmd/benchguard -count=1 -timeout=5m` | 通过 |
| CI 所列热路径基准，`-p 1 -benchtime=250ms -count=3`，随后执行 benchguard | 通过；GOMAXPROCS=4，非独占环境，不作为业务容量或修正前后性能比较 |
| 仓库外 HTTP service scaffold，`go mod tidy`、`go test -p 1 ./... -count=1`、`generate --check` | 通过，使用本地源码 replace |
| 生成服务实际启动并请求 `/healthz` | HTTP 200，`status=ready` |
| `go test ./.github/scripts -count=1`，`releasecheck validate-tag --tag v0.2.0` | 通过；未创建标签 |
| `benchmark/compare` 的测试、vet、依赖校验和漏洞扫描 | 通过；无可运行测试，扫描无可达漏洞，另有 23 个模块级报告 |
| `govulncheck@v1.6.0 ./...`，Go 1.26.4 | 未通过：报告 22 个可达漏洞，涉及标准库、x/net、x/text、gRPC |
| `gosec@v2.22.5 -exclude-generated -quiet ./...` | 工具依赖准备耗时，已中止；未获得扫描结论，发布前须重跑 |

升级前 Go 1.26.4 的默认全量测试曾出现 Windows `0xc0000005` 进程异常；纯 Go 复测中的
`benchmark/cmd/sse-loadgen` 同样异常，单包复测通过，原因尚未定位。
该次全量运行不能记为发布通过。原始本地日志存放在被忽略的
`tmp/opensource-*.log`，发布前需在干净提交上重跑并归档 CI 证据。

2026-10-19 已按维护者授权升级：最低 Go 版本为 1.26.9，x/net 为 v0.60.0，
x/text 为 v0.42.0，gRPC 为 v1.83.2。x/text v0.42.0 是 x/net 的依赖要求；
gRPC v1.83.1 修复 OOM，但仍受 GO-2026-6443 影响，因此采用 v1.83.2。
同步更新根模块、对照模块、脚手架、CI 和支持政策。Go 1.25 支持在下一 minor
版本移除；本机使用 Go 自动下载的 1.26.9 工具链，未替换系统 Go 安装。

升级前的扫描结果保留为历史记录，升级后的检查另行记录，不能相互替代。

升级后验证（Windows amd64，Go 1.26.9，GOMAXPROCS=4）：

| 检查 | 结果 |
| --- | --- |
| 根模块和 `benchmark/compare` 的 `go mod verify`、`go mod tidy -diff` | 通过，无元数据差异 |
| Go 1.26.9、CGO_ENABLED=0 下的 `go vet -p 1 ./...` | 通过 |
| Go 1.26.9、CGO_ENABLED=0 下的 `go test -p 1 ./... -count=1 -timeout=10m` | 全部通过，包括跨包集成与生成代码测试；本次未复现升级前的 Windows 进程异常，未据此断言旧异常根因已修复 |
| `govulncheck@v1.6.0 ./...` | 通过：0 个可达漏洞、0 个导入包级漏洞；依赖图仍有 5 个未导入受影响代码的模块级报告 |
| Linux amd64、CGO_ENABLED=0 条件下的 govulncheck 静态分析 | 通过：同样为 0 个可达漏洞、0 个导入包级漏洞；首次拉取漏洞库 TLS 超时，重试通过，不代表 Linux 运行测试通过 |
| `benchmark/compare` 测试及 vet | 通过，无可运行测试 |
| 更新后工具链生成的独立 HTTP scaffold，tidy、测试及 `generate --check` | 通过，使用本地源码 replace |
| Go 1.27.2 下的 core、server、middleware、codec 和全部 transport 测试 | 通过；CGO_ENABLED=0，`-p 1 -count=1 -timeout=5m` |
| Go 1.26.9 下的 core、server、全部 transport 和 observability race 测试 | 通过；CGO_ENABLED=1，`-p 1 -race -count=1 -timeout=5m` |

升级后日志存放于 `tmp/security-upgrade-*.log`。这些是本地工作区结果；
真实外部数据库集成、Linux 运行测试、完整发布产物及干净提交 CI 仍需按发布清单验证。

权属确认、GitHub 账号恢复及外部用户试用需要维护者参与。

## 公开仓库后的远程验证

2026-10-19 已核实公开仓库为 `saramand9/kpro`，默认分支为 `main`，
本地 origin 为 `git@github.com:saramand9/kpro.git`。GitHub CLI 的旧凭据
仍不可用，本次通过维护者已登录的 GitHub 网页核对设置。

- `main` 要求 PR、分支保持最新、解决讨论和 8 个必需检查，规则覆盖管理员；
  不强制单维护者获得他人审批，不允许强推或删除。
- 必需检查为 `test`、`database-integration`、`windows-smoke`、
  `go-compatibility (1.26.9)`、`go-compatibility (1.27.2)`、
  `Credential history`、`Go security` 和 `CodeQL`。
- 已设置英文简介及 golang、networking、tcp、websocket、grpc、quic、framework
  Topics；已有 bug、documentation、good first issue、help wanted 等 12 个标签。
- 已开启 Private vulnerability reporting，`SECURITY.md` 指向仓库安全报告入口。
  安全维护覆盖最新已发布 minor；旧 minor 及已被替代的 patch 不承诺维护。
- `6648bcb` 的 [CI](https://github.com/saramand9/kpro/actions/runs/39209476397)
  通过 Linux 完整测试、race、vet、覆盖率、性能预算、真实 MySQL/PostgreSQL
  集成、Windows 和两个 Go 版本检查。这是该提交的证据，不替代后续提交检查。
- 同一提交的 [Security](https://github.com/saramand9/kpro/actions/runs/39209476380)
  失败：旧 Gitleaks 镜像报告 6 项，gosec 报告 103 项，Go CodeQL 不支持
  `build-mode: none`。本分支正在处理这些问题，尚未发布标签。
- 本地固定 Gitleaks v8.30.1 对 fetch 后 245 个提交扫描通过，仍使用仅限指定
  WebSocket 测试 nonce 的既有例外。CI 工具同步到该版本，等待远程复验。

发布前须让修正后的 PR 通过所有检查，再选取合并提交创建 annotated tag。
代表性负载性能对比和独立外部试用仍未完成；公开仓库与安全检查不代表生产级 v1。

## v0.2.0 标签与公开安装验证

2026-10-19，[PR #56](https://github.com/saramand9/kpro/pull/56) 的 9 项检查
全部通过后合并为 `9eabc7a6aa570a74c234d6975c0ef78e68af58d8`。
合并提交的源码树与已验证的 `4df6ad8` 一致。

- [PR CI](https://github.com/saramand9/kpro/actions/runs/39213823126)：完整测试、
  race、vet、覆盖率、热路径预算、生成项目、数据库集成、Windows 和两个 Go 版本通过。
- [PR Security](https://github.com/saramand9/kpro/actions/runs/39213823124)：
  凭据历史、gosec、可达漏洞检查及 CodeQL 通过；PR 的代码扫描无 open 告警。
  gosec 保留逐处审查的本地 CLI 路径/工具、受检转换及 RFC 6455 SHA-1 例外，
  没有全局关闭规则。对照模块只有测试源码，默认 gosec 不扫描测试；其编译、vet
  和漏洞检查另有 CI 覆盖，CodeQL 编译对照测试二进制而不运行基准。
- Windows amd64、Go 1.26.9、CGO_ENABLED=0 的完整本地测试和 vet 通过；
  新增 TLS 信任、解压限制回归和 Windows 386 codec 边界测试通过。
- annotated tag `v0.2.0` 已推送，
  [Release 工作流](https://github.com/saramand9/kpro/actions/runs/39215248246)
  全部通过并公开 [v0.2.0](https://github.com/saramand9/kpro/releases/tag/v0.2.0)。
  Linux、Windows、Darwin 的 amd64/arm64 共 6 个归档、SHA-256 校验和及 6 个
  SBOM 通过发布工具检查；另外提供 GitHub 自动生成的两个源码归档。
- 官方模块代理的 v0.2.0 元数据返回上述合并提交，公开路径为
  `github.com/saramand9/kpro`。
- 仓库外新目录执行 `go get github.com/saramand9/kpro@v0.2.0` 和
  `go install github.com/saramand9/kpro/cmd/kpro@v0.2.0` 均通过。
  `GOPROXY=https://proxy.golang.org,direct`、`GOSUMDB=sum.golang.org`，未禁用校验；
  本机 Go 进程需沿用既有 Windows 系统代理才能连接这两个官方服务，未修改全局设置。
- 已安装 CLI 创建独立 HTTP service，依赖精确 v0.2.0，无本地 replace。
  tidy、依赖验证、全部包编译测试、`generate --check` 通过；实际 `/healthz`
  返回 HTTP 200 和 `{"dependencies":"none","status":"ready"}`。
- pkg.go.dev 当次尚未收录该版本，手动收录请求及额外的 GitHub Dependency graph /
  Dependabot alerts 设置等待维护者单独授权；未把它们记为已完成。

独立外部使用者试点和介绍文章的发布渠道仍需维护者提供。现有验证证明开发者版本
可安装和运行；M0 的代表性业务负载对比及后续生产验收保持未完成。

### 默认分支首次完整 CodeQL 的后续发现

PR 新增告警列表为空，不代表完整默认分支无告警。`9eabc7a` 的首次默认分支
扫描随后报告 [#1](https://github.com/saramand9/kpro/security/code-scanning/1) 和
[#2](https://github.com/saramand9/kpro/security/code-scanning/2) 两条反射 XSS 告警。
实际 HTTP 回归确认：`Write`、`Bytes`、`Stream`、`CopyBody` 未设置响应类型时，
回显的 HTML 会被识别为 `text/html`。本次修正默认二进制响应类型并设置 nosniff，
准备以 v0.2.1 安全补丁交付，不重写已公开的 v0.2.0 标签。

显式选择 HTML 的应用仍需转义不可信输入；框架不应改变原始响应字节来代替
应用的 HTML 模板和转义策略。后续验收应同时核对工作流结果、PR 新增告警和
默认分支完整告警列表，不能只以任务退出码推断扫描告警已清零。

默认值修正后，真实 HTTP、HTTP3/SSE race 及既有 HTTP 基准预算通过，但
默认分支仍报告同一对原始字节写入告警。核对告警版本的
[CodeQL net/http 模型](https://github.com/github/codeql/blob/a7057b836bf29767461026718b14d20790b9085f/go/ql/lib/semmle/go/frameworks/stdlib/NetHttp.qll)
可见其通过局部响应 writer 关联 Header 与 Write，无法识别这里跨 Context
辅助方法提交的响应头。后续将头提交和原始字节写入集中到同一内部边界，
保持 header-only、Write(nil)、返回错误、显式类型、文本和流式快路径的语义；
不关闭查询，也不更改原始响应内容。完整默认分支复扫结论另行核对。
