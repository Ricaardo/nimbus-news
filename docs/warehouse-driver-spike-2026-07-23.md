# Warehouse Go DuckDB read-only spike — 2026-07-23

## 结论

状态：`prepared/driver_compatible_readonly`。

`github.com/marcboeker/go-duckdb v1.8.5` 在 `darwin/amd64`、CGO enabled、Go 1.26.2 下可用 `GOPROXY=off` 编译。其嵌入 DuckDB `v1.1.3`，以 DSN `?access_mode=read_only` 成功打开 Python DuckDB `1.5.3` 创建的小库、读取同版本生成的 Parquet，并只读打开三座现有 warehouse 数据库。

这只证明当前文件格式与只读查询的离线兼容性。driver **没有**接入 `internal/warehouse`、`cmd/nimbusd` 或正式 `go.mod`，也不代表 Go candidate 已取得生产 serving/ownership。

## 原始检查结果

```text
python_version=1.5.3
version=v1.1.3 access_mode=read_only tables=1 parquet_rows=2

ah_screener.duckdb:
version=v1.1.3 access_mode=read_only tables=24 parquet_rows=2 sample_table=main.capital_flow sample_row=true

global_history.duckdb:
version=v1.1.3 access_mode=read_only tables=20 parquet_rows=2 sample_table=main.company_documents sample_row=false

us_screener.duckdb:
version=v1.1.3 access_mode=read_only tables=23 parquet_rows=2 sample_table=main.capital_flow sample_row=false
```

执行范围仅为 `version()`、`current_setting('access_mode')`、`information_schema.tables` count、每库排序后首表 `LIMIT 1`，以及临时小 Parquet 的 count；没有执行 DDL/DML，没有运行三库全量 export。

## 后续接入门槛

- 固定可复现的 CGO toolchain 与 driver artifact；补齐升级/回滚兼容矩阵。
- 在不可变 snapshot 上做查询语义、decimal/timestamp/null、并发和资源上限测试。
- 完成 shadow/soak、校验 release manifest 与 operator go 后，才允许讨论替换 `warehouse-svc:8814`。
