# 文档索引

当前文档只保留运行和后续维护需要的内容。

## 运行维护

- [部署说明](deployment.md): 本地配置、Docker Compose、健康检查和数据目录约定
- [行情适配器](market-adapters.md): 行情数据源优先级、Longbridge 覆盖/符号格式、token 运维
- [路线图](roadmap.md): 后续待办与已完成事项

## 方案文档

- [A 股基准数据底座规划](baseline-design.md): 聚合报告数据底座的后续方案

## 清理约定

- 运行配置只维护 `config.yaml.example`，本地实际配置为 `config.yaml`
- 密钥只维护 `.env.example`，本地实际密钥为 `.env`
- 服务入口只保留 `cmd/platform`
- 部署入口只保留根目录 `docker-compose.yml` 和 `Makefile`
- `data/`、`bin/`、`web/node_modules/`、根目录二进制和报告产物不提交
- 已完成或失效的历史分析、竞品、宣传和一次性报告不再保留在仓库
