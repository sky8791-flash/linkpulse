# LinkPulse

一个以 Go 标准库 HTTP 服务为基础的短链分析项目。短链跳转走进程内 TTL
缓存，点击事件进入有界队列后批量写入 PostgreSQL，管理接口使用 Bearer API
Key，附带无需前端构建的演示页面。

## 功能

- 自动短码、自定义别名、过期时间、启停和软删除
- `302` 公开跳转、进程内缓存和过期/停用语义
- 点击趋势、唯一访客、来源域名、设备和浏览器统计
- 有界队列、批量写入、失败重试、背压计数和优雅排空
- 管理 API 鉴权、按客户端 IP 限流、请求大小限制和安全响应头
- 原始 IP 不落库，访客标识使用 HMAC-SHA256
- 结构化日志、存活/就绪检查、PostgreSQL 嵌入式迁移
- 标准库 `embed` 演示页面、OpenAPI 文档、Docker 和 GitHub Actions

## 架构

```mermaid
flowchart LR
    B[Browser / HTTP client] --> H[net/http + middleware]
    H -->|GET /{code}| C{Process-local TTL cache}
    C -->|hit| S[Link service]
    C -->|miss| P[(PostgreSQL)]
    S --> R[302 redirect]
    R --> Q[Bounded click queue]
    Q --> W[Batch collector]
    W -->|CopyFrom every 1s or 100 events| P
    H -->|Bearer API key| A[Management API]
    A --> S
    U[Embedded dashboard] --> A
```

核心接口在业务层做接口隔离：`LinkStore`/`StatsReader` 由 PostgreSQL
实现，`LinkCache` 由进程内 TTL 缓存实现，`ClickRecorder` 由异步 collector
实现。HTTP 层不直接依赖数据库类型。

## 数据模型

`links` 保存短码、目标 URL、过期时间、启用状态和软删除时间。`click_events`
保存点击时间、64 位访客 HMAC、来源域名、设备和浏览器；索引以
`(link_id, occurred_at DESC)` 支持按短链和日期范围聚合。

统计默认使用 `Asia/Shanghai` 时区。唯一访客是范围内访客哈希的精确
`COUNT(DISTINCT ...)`，不是原始设备或用户识别。

## 本地运行

前置条件：Go 1.27、PostgreSQL 18。当前机器数据库安装在
`D:\PostgreSQL\18`，服务名为 `postgresql-x64-18`。

### 一键启动与关闭

直接双击项目根目录的 `start.bat`。脚本会自动：

- 从 `.env.example` 创建本地 `.env`（如果尚不存在）
- 启动本机 PostgreSQL 18 服务（如果未运行）
- 编译并以隐藏窗口启动 LinkPulse
- 等待 `/readyz` 成功后打开浏览器
- 把 PID、日志路径写入 `.run/server.json`

停止时双击 `stop.bat`。脚本会校验 PID 对应的确实是本项目的
`bin\linkpulse.exe`，等待最多 2 秒让点击批次落库，然后关闭进程，避免误杀其他
程序。运行日志保存在 `logs/`，两个运行时目录都已加入 `.gitignore`。

创建本地角色和数据库：

```powershell
$psql = 'D:\PostgreSQL\18\bin\psql.exe'
& $psql -U postgres -d postgres -c "CREATE ROLE linkpulse LOGIN PASSWORD 'linkpulse_dev_password';"
& $psql -U postgres -d postgres -c "CREATE DATABASE linkpulse_dev OWNER linkpulse;"
& $psql -U postgres -d postgres -c "CREATE DATABASE linkpulse_test OWNER linkpulse;"
```

如果角色或数据库已存在，跳过对应命令。然后设置环境变量并启动：

```powershell
$env:DATABASE_URL = 'postgres://linkpulse:linkpulse_dev_password@127.0.0.1:5432/linkpulse_dev?sslmode=disable'
$env:API_KEY = 'local-demo-api-key-123456'
$env:BASE_URL = 'http://localhost:8080'
$env:VISITOR_HASH_SECRET = 'local-demo-visitor-secret-123456'
go run ./cmd/server
```

打开 `http://localhost:8080`，输入同一个 API Key 后即可创建和查看短链。
服务启动时会自动执行幂等嵌入式迁移。配置项和默认值见
[`.env.example`](.env.example)；程序直接读取环境变量，不会自动加载 `.env`。

## Docker

```powershell
Copy-Item .env.example .env
docker compose up --build
```

Compose 会启动 PostgreSQL 18 和应用，并持久化数据库卷。Compose 内部会把
`DATABASE_URL` 指向 `postgres` 服务；当前开发机没有安装 Docker，因此容器构建
由 GitHub Actions 验证。

## API

完整契约见 [`internal/httpapi/openapi.yaml`](internal/httpapi/openapi.yaml)。
管理接口统一使用 `Authorization: Bearer <API_KEY>`。

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/links` | 创建短链 |
| `GET` | `/api/v1/links` | 游标分页查询 |
| `GET` | `/api/v1/links/{code}` | 链接详情 |
| `PATCH` | `/api/v1/links/{code}` | 修改目标、过期时间或启停状态 |
| `DELETE` | `/api/v1/links/{code}` | 软删除，重复删除保持幂等 |
| `GET` | `/api/v1/links/{code}/stats` | 点击统计 |
| `GET` | `/{code}` | 公开跳转 |
| `GET` | `/healthz`、`/readyz` | 存活与就绪检查 |

创建短链：

```powershell
curl.exe -X POST http://localhost:8080/api/v1/links `
  -H "Authorization: Bearer local-demo-api-key-123456" `
  -H "Content-Type: application/json" `
  -d '{"target_url":"https://go.dev/doc/","custom_alias":"go-notes"}'
```

查询统计：

```powershell
curl.exe "http://localhost:8080/api/v1/links/go-notes/stats?from=2026-11-01&to=2026-11-11" `
  -H "Authorization: Bearer local-demo-api-key-123456"
```

错误统一返回：

```json
{"error":{"code":"invalid_target_url","message":"target_url must be an absolute http or https URL up to 2048 characters"}}
```

## 关键取舍

- 统计是**最佳努力**模型。队列满时不会阻塞跳转，而是丢弃统计并累计计数；
  正常情况最多约 1 秒延迟。需要零丢失时应改用持久化消息队列。
- 缓存是**进程内**有界 TTL/FIFO 缓存。单实例下更新/删除会主动失效；多实例
  部署需要 Redis、版本号或消息广播来保持一致。
- API Key 是单租户方案，不提供用户注册、JWT、多租户隔离或细粒度权限。
- 软删除保留统计明细，但普通管理 API 不再返回已删除短链。
- 限流按直连 `RemoteAddr` 计数，不信任 `X-Forwarded-For`，避免默认信任可伪造
  的代理头。部署在反向代理后时需要显式设计可信代理边界。

## 测试

单元测试无需数据库：

```powershell
go test ./...
```

PostgreSQL 集成测试使用独立临时 schema，测试结束自动 `DROP SCHEMA`：

```powershell
$env:TEST_DATABASE_URL = 'postgres://linkpulse@127.0.0.1:5432/linkpulse_test?sslmode=disable'
go test ./... -count=1
go vet ./...
```

Linux CI 还会执行 `go test -race ./...`。当前 Windows 环境未启用 cgo，因此
本机不能运行 race detector。CI 同时执行格式检查、集成测试和 Docker 构建。

## 基准结果

以下结果来自本机真实运行，不是理论值：

```text
Go 1.27.1 / windows amd64 / Intel i5-12450H / 2026-11-11

BenchmarkResolveCacheHit-12            137.9–141.8 ns/op    0 B/op    0 allocs/op
BenchmarkResolveCacheHitParallel-12     27.24–28.45 ns/op    0 B/op    0 allocs/op
```

该基准只覆盖“短链服务命中进程内缓存并返回目标 URL”的核心路径，不包含
HTTP 网络、TLS、PostgreSQL 查询或点击落库，不能等同于端到端 QPS。

复现：

```powershell
go test ./internal/link -run '^$' -bench 'BenchmarkResolveCacheHit' -benchmem -count=3
```

## 目录

```text
cmd/server             应用入口、启动和优雅退出
internal/config        环境配置与校验
internal/httpapi       HTTP 路由、中间件、演示页、OpenAPI
internal/link          短链业务规则
internal/cache         进程内 TTL 缓存
internal/analytics     点击元数据、异步批量 collector
internal/store/postgres PostgreSQL 仓储和嵌入式迁移
internal/ratelimit     按 key 的令牌桶
```
