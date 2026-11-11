# 简历项目描述

## LinkPulse · Go 短链分析服务

**技术栈：** Go、标准库 `net/http`、PostgreSQL、pgx、Docker、GitHub Actions

- 使用 Go 标准库实现短链 REST 服务，完成自动短码、自定义别名、过期、启停、
  软删除、Bearer API Key 鉴权和内嵌演示页面。
- 为公开跳转设计进程内有界 TTL 缓存快路径；本机基准中缓存命中约
  `138–142 ns/op`、0 次内存分配，并行基准约 `27–28 ns/op`。
- 使用有界队列和后台 worker 聚合点击，按 100 条或 1 秒批量写入 PostgreSQL；
  队列满时快速降级，不阻塞跳转，并在关闭时排空队列和连接池。
- 以 HMAC-SHA256 保存访客匿名标识，不落原始 IP；提供按天点击趋势、唯一访客、
  来源、设备和浏览器统计。
- 使用独立临时 schema 的 PostgreSQL 集成测试覆盖迁移、唯一约束、分页、软删除
  和统计聚合，并通过 GitHub Actions 执行格式、vet、测试、race detector 与
  Docker 构建。

> 面试时只引用 `README.md` 中记录的实际基准结果，并明确它是缓存核心路径的
> 微基准，不代表端到端 QPS。
