# nimbusctl candidate 运维控制面

`nimbusctl` 是 JSON-first、默认只读的 candidate 运维入口。它不包含 `launchctl` 调用，也没有生产变更实现；生产标签、已登记/退役端口以及 candidate 根目录之外的路径都会硬拒绝并返回 `PRODUCTION_MUTATION_FORBIDDEN`。

配置以 `config/system.yaml` 为入口。端口注册表不能覆盖：路径必须精确派生为 workspace sibling `docs/ports.yaml`，一次打开后保存 raw bytes 与 SHA-256 快照；release、preflight、plan、token 和 apply 都绑定同一注册表哈希。配置 loader 拒绝未知字段、重复 YAML key、`${ENV}`、shell 片段以及 secret/token/password 一类字段；外部命令只能用 argv 数组表达。两个可选运行时默认关闭。

## 安全工作流

1. 手工创建与 `candidate.root` 精确一致的 0700/0750 隔离目录后，用 `nimbusctl candidate init` 写入 0600 marker，并以 `O_EXCL` 生成 32-byte 0600 HMAC key。key 内容永不输出。生产目录及其祖先即使预放 marker 也会被拒绝。
2. `nimbusctl status`、`doctor`、`contracts check` 只读检查配置、端口和契约。
3. `release build` 只允许固定 workspace 的 `news`、`datasources`、`guanfu` 仓库和固定 `go build -trimpath` 目标 `nimbusd`、`nimbusctl`。任意 `--repo`、`--nimbusd`、`--nimbusctl` 或 prebuilt 路径都不存在。任一仓库 dirty 时在 builder 执行前拒绝；构建只读取 captured Git HEAD 物化到 candidate 临时目录的源码快照，并强制使用生成且哈希绑定的隔离 `GOWORK`，不读取 workspace 的 ambient `go.work`。清单绑定全部仓库、工具链、config hash、ports registry hash、workspace hash 和内容寻址二进制。
4. `evidence record/verify` 使用内部加锁的 compare-and-append 哈希链，调用者不能传 `previous_hash`。五种证据 `external`、`shadow72h`、`soak24h`、`backup_restore`、`rollback_retention7d` 缺一不可；外部证据还必须有 key ID/有效期，恢复证据必须绑定实际 backup ID。
5. `backup` 的对象集合只来自配置的 `backup.required_objects`，固定为 `news`、`control`、`signals`、`shadow_feed`，不能为空或由 CLI 覆盖。`nimbusctl` 只连接 `<candidate.root>/run/snapshot-v1.sock`，校验 0600 Unix socket、当前 UID 与 candidate root/config 绑定后，发送一次不含 owner/object list/set ID 的 `snapshot_set` 请求。`nimbusd` 生成 set ID，并按固定顺序调用 Bolt、SQLite、signal JSONL、shadow feed 的内存 owner 接口；任一 owner 失败、元数据不符、越界或连接中断都会终止整组且不发布 manifest。对象先流入 candidate capability 管理的 staging，经过 size/SHA-256、fsync 和原子 CAS 后才可见。只有四个 owner receipt 属于同一 set 且全部自校验通过，才发布包含 set receipt 的 `nimbus-backup/v2` manifest；不存在 pathname fallback。`restore-verify` 使用同一受信对象集合，只恢复到全新隔离目录，并持久化绑定 root、backup ID 和对象集合（含嵌套 owner receipts）的自哈希 receipt；receipt 验证会从恢复目录逐对象重算 size/SHA-256，`backup_restore` evidence 必须引用该 receipt。
6. `preflight` 重新校验当前 clean source、release artifacts、config/registry hash、完整 backup objects 和证据链。缺少 manifest 时返回稳定 JSON refusal。
7. `cutover plan` 不接收 caller gate bool；只有所有受信输入通过才生成，并绑定 release/config/ports/evidence tail/backup/root/真实 `os.Hostname()`。operator token 使用 HMAC-SHA256，绑定 action、plan 及全部输入，最长 15 分钟；apply 再次加载并验证所有输入，以 `O_EXCL` nonce CAS 拒绝重放。
8. `apply`/`rollback` 只在 candidate root 内写 receipt，不调用 launchd，不监听或修改登记/退役端口。production label、root 外路径和已登记端口统一返回 `PRODUCTION_MUTATION_FORBIDDEN`。

## 当前门禁状态

owner-mediated snapshot 已接入 candidate `nimbusd`，因此只有该 daemon 正在运行、socket 生命周期与配置/root 绑定一致时，`backup` 才可能成功；离线、旧 socket、身份或 receipt 不匹配均硬拒绝且不发布 manifest。72 小时 shadow、24 小时 soak、7 天 rollback retention、真实外部凭据和已验证的 backup/restore evidence 仍必须全部满足，`preflight` 才会放行；门禁失败属于预期的 fail-closed 状态。

候选 plist `deploy/com.nimbus.nimbusd-candidate.plist` 的 `RunAtLoad=false`，不含凭据或登记端口。本阶段只允许 `plutil -lint`，不得 load/start。
