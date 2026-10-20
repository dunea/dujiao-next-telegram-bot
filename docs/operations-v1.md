# v1.0 开发版：安装、升级、回滚和故障排查

适用 `1.0.0-dev` 源码/自行构建的候选程序，**不是 v1.0 正式发布说明**。已发布的 v0.4.0 不包含 `setup` 命令，继续使用 [v0.4.0 运维说明](../RELEASE.md)。本指南不自动部署、备份、迁移、解锁资金操作或回滚；真实 Telegram、支付沙箱与商家运维验收仍需单独完成。

## 一、离线安装向导

在本项目源码根目录运行，或把 `go run ./cmd/bot` 换成当前候选程序路径：

```bash
go run ./cmd/bot --version
go run ./cmd/bot setup --help
go run ./cmd/bot setup init --env-file .env
```

`init` 只创建一个**不存在的新文件**，权限 `0600`，使用随程序嵌入的公开 `.env.example` 和新的独立随机 32 字节会话密钥。它不读取旧配置、不生成商店凭证、不启动服务。已有文件、符号链接、目录或缺失的父目录均拒绝，不能用它“修复”旧密钥。极少见的写入失败可能留下私有的不完整新文件，需自行检查，工具不会覆盖或删除它。

已有部署跳过 `init`，继续使用原配置、密钥和存储。先备份并核对未决业务，不要通过删除旧 `.env` 再初始化来解除保护。

用本机编辑器私下填写 `.env`：

- `DUJIAO_BASE_URL`：自己的商店根地址，建议 HTTPS；不能保留示例地址，不在 URL 内放凭证、查询参数或片段。
- `DUJIAO_CHANNEL_KEY` / `DUJIAO_CHANNEL_SECRET`：商店后台 **Telegram Bot 渠道**凭证，不是管理员登录口令、支付网关密钥或前台 API Key。
- `TELEGRAM_BOT_TOKEN`：可留空，由商店配置提供；离线检查不会获取或验证远端 Token。
- `BOT_SESSION_ENCRYPTION_KEY`：首次生成后稳定保留、独立备份，不复用渠道 Secret 或公开演示密钥。Redis 模式下，丢失/更换它可能无法读取已有草稿与保护。
- `BOT_ADMIN_USER_IDS`：管理员先与自己的测试 Bot 私聊，用 `/whoami` 获取私人数字 ID；不填群组 ID/用户名。

启用随仓库提供的 Redis 覆盖配置前，离线检查：

```bash
go run ./cmd/bot setup check --env-file .env --profile compose-redis
```

不用此覆盖文件时用 `--profile runtime`，按实际设置填 `BOT_REDIS_URL`。留空代表内存模式：工具会警告重启不保留保护；语法检查通过不代表适合实际提现/群发部署。

### 检查输出与作用范围

输出固定 JSON，`configuration_ok: true` 表示配置检查通过，非零退出表示失败；警告必须人工阅读。`init` 的 `status: created` 仅表示创建成功，其 `configuration_ok` 仍为 false，必须填写后再检查。报告不输出变量值、Token、密钥、文件路径或原始错误，Redis 模式会输出与运行时一致的命名空间哈希，供前后核对；不会输出加密密钥指纹。

检查始终为 `network_checks: not_run`。它不访问商店、Telegram 或 Redis，不创建 Redis epoch，不检查历史数据、磁盘耐久性、端口占用或审计目录写权限。`setup` 也不加载其他环境文件、执行 shell、发送通知或发起资金请求。

- 不传 `--env-file`：只检查当前进程环境，**不会自动加载 `.env`**。
- 显式文件：只读该文件，不与宿主环境混合。要求普通文件、权限不允许组/其他用户访问（通常 `chmod 600 .env`），不跟随文件符号链接。
- 文件支持严格的单行 `KEY=value` 子集：整行注释、空行、CRLF、简单引号；单引号内 `$`/`#` 为字面值。拒绝重复键、未知变量、插值、反斜杠转义、内联注释、多行值与 `export`。只接受模板变量及可选 `COMPOSE_PROJECT_NAME`；其他部署变量请用独立受控的注入方式。
- 这是有意受限的检查格式，不是完整 Compose dotenv 解释器。复杂现有配置不要通过 `source .env` 或 `export $(cat .env)` 导入；由原服务管理器/Compose 注入后检查真实进程环境。

`compose-redis` 是**仓库标准覆盖文件的静态模型**：它采用固定内部 Redis URL 和审计路径，存在自定义 Redis URL 时提示被覆盖，不代表探测到实际 Docker 配置。Compose 的 `environment`、`env_file` 和宿主插值有优先级；自定义覆盖、`-e`、宿主导出的密钥也可能改变最终值。以[官方环境优先级](https://docs.docker.com/compose/how-tos/environment-variables/envvars-precedence/)和[插值规则](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)为准，不公开粘贴 `docker compose config` 的展开输出。

## 二、新部署启动与验收

下列命令针对本仓库标准 Compose；自定义服务名/网络/项目名需使用自己的实际配置。程序本身不读取 `.env`，Compose 按配置注入。保持单 Token 单个长轮询实例，不要在旧实例未停用时启动新副本。

```bash
docker compose -f compose.yml -f compose.redis.yml build bot
# 一次性离线检查容器最终注入值：不启动 Bot，不连接商店或 Redis
docker compose -f compose.yml -f compose.redis.yml run --rm --no-deps bot setup check
docker compose -f compose.yml -f compose.redis.yml up -d --wait
docker compose -f compose.yml -f compose.redis.yml exec -T bot /usr/local/bin/dujiao-bot --version
curl --fail http://127.0.0.1:8444/healthz
```

不要将运行的 `bot` 容器替换成只执行 `setup check` 的命令；检查用 `run --rm --no-deps` 或 `exec` 子进程。默认端口映射为 `8444:8444`，部署者应通过防火墙/反向代理限制入口；不要因此开放 Redis 端口。回调地址必须可从商店访问，公网入口使用 HTTPS；同一可信内网可使用内部地址。机器人长轮询无需 Telegram Webhook。

`/healthz=200` 仅证明健康接口和会话存储检查可用，不证明商店、支付或 Telegram 全部正常。联网只读诊断单独运行 `dujiao-compatcheck`，见[兼容性矩阵](compatibility.md)；这仍不是支付/提现写入验收，不等同于实际发行版本识别。

首次上线前，按本文末尾清单做实际测试。不要为测试重建已有演示商店、清空 Redis、直接改账本或提高限额；本仓库的 `make telegram-e2e` / `make soak-test` 是独立合成栈验收，不作用于真实商店。

## 三、升级前检查与备份

所有步骤在原部署目录、原配置和原 Compose 项目名下进行；如果原调用带 `-p`/`--project-directory`，所有步骤保留它们。目录或项目名变化可能创建新卷，配置哈希比较无法证明卷相同。

1. 记录当前程序版本、源码提交、运行镜像 ID、Compose 调用方式和实际卷映射；处理未提交自定义代码，不覆盖工作区。选定并验收候选提交/镜像，不用漂移的 `latest` 作为回滚标识。
2. 查看商店实际支付、充值、提现状态；记录未决操作、群发进度及回调队列积压。暂停剩余公告任务。结果不明的提现不能以重启或换命名空间来解锁。
3. 停止 **Bot**，防止新的用户资金请求；商店与第三方网关可能仍有异步操作，必须按实际系统核对，不能由 Bot 停机推断它们也停止。

   ```bash
   docker compose -f compose.yml -f compose.redis.yml stop bot
   ```

4. 使用自己的备份设施保存 `.env`、稳定加密密钥、商店数据库及稳定服务端密钥、Bot Redis 一致性数据、审计卷、当前代码/镜像标识。备份放独立私有位置，建议加密；不要上传到 GitHub 或 CI。
5. Redis 文件级备份必须有一致性保障；专用 Redis 可停机后复制完整数据目录，再原样恢复服务。共享/托管实例按其快照规则执行，不擅自停止其他业务。Redis 7 的多段 AOF 不能只复制一个增量文件；保存相应目录/清单和所需数据，参考[Redis 持久化与备份](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)。本项目不会替外部 Redis 修改持久化或淘汰设置。
6. 在不接入真实 Token/支付服务的隔离环境验证备份可恢复。仅看到备份文件存在不算恢复成功；不能把真实 Redis 恢复副本直接接回生产商店来做演练。

用候选程序离线比较当前文件与**升级前保存的私有配置**（示例路径需换成自己的备份位置）：

```bash
go run ./cmd/bot setup check --env-file .env --profile compose-redis --compare-env /secure/backup/.env.before-upgrade
```

比较商店 URL、Channel Key、Redis URL、会话加密密钥和文件中的 `COMPOSE_PROJECT_NAME`。变化会返回 `changed_requires_manual_review`；仅允许与运行时一致的末尾斜杠归一化及等价十六进制大小写。它不自动批准变化、不迁移数据，也无法识别 DNS 指向变化、相同地址下的空库、数据回滚、卷更换或目录推导的项目名变化。

比较文件与容器最终注入环境不是一回事，特别要核对宿主环境/秘密管理器覆盖的会话密钥；不要为消除警告重生成密钥。已有 v0.4.0 没有比较命令，可在旁边运行新候选的离线命令，**不启动第二个 Bot**。

## 四、切换候选与回滚

完成备份与候选验收后，固定已选择的源码提交/标签或不可变镜像。这里不提供未经选择的目标值或一键自动升级脚本。源码切换时保留 `.env`、密钥、原项目与卷，不覆盖自定义文件；预先记录的旧提交/镜像是回滚目标。

对源码构建的标准 Compose，更新候选代码后只重建/启动 Bot：

```bash
docker compose -f compose.yml -f compose.redis.yml build bot
docker compose -f compose.yml -f compose.redis.yml run --rm --no-deps bot setup check
docker compose -f compose.yml -f compose.redis.yml up -d --no-deps --wait bot
```

Redis 如为备份被停机，先恢复原服务并检查健康，再启动 Bot；上述 `--no-deps` 不会替你启动 Redis。商店数据库升级是独立任务，遵循上游固定版本迁移和备份恢复规则，不能用 Bot 升级命令代替。

升级后核对版本、健康、命名空间、管理员权限、历史订单/余额/提现保护、未决群发和实际回调。完成后才恢复业务。审计中异常/保护记录及服务端账本必须一并看，不以“聊天没有消息”推断付款失败。

若失败：

1. 停止 Bot，保留失败现场、当前配置和当前数据副本，先核对已发出的操作。
2. 优先只回滚程序到已知能读取**当前**安全状态的版本，仍使用原 Redis/密钥/卷；旧版本能启动不代表它保留新通知防重或故障语义。
3. v0.4.0 的存储往返已有测试，但旧版不识别 v1.0 新通知回执及 429→503 语义；回滚可能重复/漏通知。v0.3 及更早版本不能代替当前的 Redis 提现保护。无法确认时保持停止并人工处理。
4. 不为回滚代码而恢复旧 Redis 快照：那会回退未决保护、群发游标、退订和通知回执，可能重发已经发生的操作。必须恢复数据时，先由商家协调账本/队列/网关核对和一致性恢复，不能自动重放资金请求。

禁止用 `down -v`、删键、换 Channel Key/URL、换密钥、换项目名或空内存状态来“修复”升级。演示栈使用自己的原项目名与 `compose.demo.yml`，只更新 Bot；`demo-store` 在内存保存数据，不应被重建。本轮工具不会执行这些动作。

## 五、故障排查速查

| 现象 / 检查码 | 先检查与处理 |
| --- | --- |
| `new_file_required` | 目标已存在/不安全或父目录不存在；旧部署跳过 init，不删除旧配置重新生成 |
| `private_permissions_required` / `regular_file_required` | 使用自己的普通私有文件（通常 0600）；不改成全员可读、不使用符号链接/管道绕过 |
| `unknown_variable` / `duplicate_variable` / `expansion_or_escape_rejected` | 看行号，本机编辑严格子集；复杂 Compose 注入后使用进程模式，不执行配置文本 |
| `invalid_runtime_settings` / `invalid_duration` / `out_of_range` | 核对对应变量及 `.env.example` 的范围；不把原始敏感输入粘贴到工单 |
| `changed_requires_manual_review` | 停止升级，核对命名空间、原 Redis/数据库、密钥与项目名，不自动批准迁移 |
| `/healthz=503` / `session storage unavailable` | 查 Redis 原实例、连接/认证、数据集标识、卷和持久化；恢复原数据，不能切回内存绕过 |
| 加密密钥不匹配 / 状态损坏或未知版本 | 恢复匹配的原密钥/数据副本；不要清空，保留现场再核对业务 |
| 商店 401/403 | 核对渠道类型、Key/Secret、接口授权和商店配置；Token 不是 Channel Secret，日志不打印凭证 |
| Telegram 409 / 无法收取更新 | 检查同 Token 的其他轮询实例/已有 Webhook；由管理员确认停止冲突来源，避免启动更多副本 |
| Telegram 429 / 服务端临时错误 | 等待冷却/有限重试，核对队列及保护状态；不绕过限额。公告结果不明须原创建者核对，不自动重发 |
| 商店回调 401 | 核对双方时间（默认允许前后五分钟）、签名方法/路径/正文及渠道密钥；代理不能改写被签名内容 |
| 回调 503 | 看 `status`；`delivery_busy` 为租约/冷却，`receipt_unavailable` 再按 `stage=claim/finalize` 查固定分类诊断，详见[专项说明](callbacks-and-e2e.md#重启回调专项与脱敏诊断)；恢复后由上游队列重试，不重放支付/充值请求 |
| 回调来源限流 429 / 无通知 | 固定官方版本对 4xx 不重试；检查实际流量/入口规则和上游任务记录，人工对账，不以重新付款修复消息 |
| 自动卡密未交付 | 查商店付款状态、队列、对应 SKU 可用/预占卡密和交付记录；错误支付签名或不足库存不能靠重复付款绕过 |
| 提现结果不明 | 查看服务端提现记录、联系商家核对；只允许用户确认后解除保护，不自动再次提交 |
| 公告暂停 / prepared | 原创建者用 `/broadcast_status` 核对并继续未领取受众或停止；保留订阅和未决记录 |
| 管理员无通知 / 审计无法写入 | 核对私聊数字 ID、屏蔽状态、`admin.delivery`、审计卷与运行 UID 权限；不设 0777 或公开上传审计全文 |

通知回执保留七天、最多 10000 个资源，满容量或存储异常会保守拒绝；投递租约两分钟。响应丢失时仍可能重复通知，也可能在上游重试耗尽后需要人工处理；不承诺消息/资金全局恰好一次。详见[回调保证边界](callbacks-and-e2e.md)。

## 六、真实商家沙箱验收清单（尚未代执行）

只使用专用测试 Bot、商家授权的沙箱/测试商店、合成用户/收款信息及提供方认可的测试金额。凭证通过本机私有文件配置，不发到频道、仓库或普通工单；不要复用生产 Token 与资金账户做压测。

- [ ] 明确负责人与测试环境；记录候选版本、官方后端版本、数据库类型、渠道和回滚标识，确认可恢复备份。
- [ ] 实际 Telegram `/start`、语言、商品/SKU/数量、在线/余额购买、订单查询/取消；人工交付与自动卡密完整可读且不会串用户。
- [ ] 充值预设/自定义金额、手续费与实付一致，签名回调入账一次；提供方沙箱重复通知不重复记账。
- [ ] 真实网络下支付/交付/充值通知、反向代理路径、TLS、时钟与回调重试，核对商店账本、任务记录及聊天结果。
- [ ] 礼品卡、推广归因、最低提现与私有收款信息填写/二次确认；仅测试待审核申请，不审核、不打款，不以实际转账验证保护。
- [ ] 管理员白名单、脱敏审计、主动订阅/退订和小范围自愿测试公告；不向真实买家自动群发。
- [ ] 经批准的停启/恢复演练保留原密钥、卷、命名空间、未决提现/群发/回执；全程单长轮询实例，不自动解除保护。
- [ ] 商家与维护者确认异常/待办及回滚条件，保存脱敏记录（结果/时间/测试编号），不附 Token、卡密、支付链接或完整收款信息。

这些清单不是通过记录。当前自动化证据为[兼容性/迁移](compatibility.md)、[隔离回调 E2E](callbacks-and-e2e.md)和[持续压力/自动卡密](soak-and-auto-delivery.md)；完成真实沙箱、未完成的容量/提供方范围和发布门槛之前，不宣称 v1.0 正式稳定发布。

## 本批自动化复验与待观察项

2026-10-20 本地 Docker Desktop Linux ARM64：向导的独占创建/0600 权限、只读与脱敏、配置不执行不插值、零 HTTP/Redis 连接、比较不改变输入、并发环境加载与历史命名空间回归通过；`make verify`、两种数据库真实升级链、人工/自动交付 E2E、各 90 秒持续负载和实际 Bot 镜像的离线命令通过。Linux AMD64 结果以同提交 CI 为准，不由本机结果推断。

首轮重启后重复通知回调曾出现一次 `503`，当时测试未保留正文；之后完整重跑与额外三次 SQLite 全链路未复现。现在进一步拆分未决/存储故障响应，补齐领取/完成阶段、固定原因采样日志、取消后诊断和连续五次重启专项，不在 CI 打印任意服务端正文。对照实验已定位并修正测试隧道的 EOF 半关闭问题（可提前取消后端请求），但不能完全归因历史那一次 503；保持 [ROADMAP](../ROADMAP.md) 待核实状态。不将一般 503 当作成功，不为真实部署添加盲目重试。
