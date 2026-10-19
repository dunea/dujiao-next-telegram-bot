# v0.4.0 发布与运维

社区版，MIT 许可，自托管免费。此版本包含推广与提现申请、Redis 会话和重启保护、管理员通知、操作审计、分层限流及主动订阅的纯文本公告群发。完整功能及限制见 [README](README.md) 和 [变更记录](CHANGELOG.md)。

## 下载与版本核对

从 [v0.4.0 Release](https://github.com/dunea/dujiao-next-telegram-bot/releases/tag/v0.4.0) 下载对应系统及架构的压缩包，同时下载 `checksums.txt`。Linux 提供 `amd64 / arm64`，macOS 提供 `amd64 / arm64`。这些程序不需要 Go 环境；Docker 用户可从固定源码标签自行构建，不提供已发布到镜像仓库的镜像。

```bash
# Linux（下载全部包时可直接验证整个清单；仅下载一个包时核对其对应条目）
sha256sum dujiao-next-telegram-bot_v0.4.0_linux_amd64.tar.gz
# macOS
shasum -a 256 dujiao-next-telegram-bot_v0.4.0_darwin_arm64.tar.gz
```

将输出与 `checksums.txt` 中对应文件的摘要比较后再解压。每个包仅含程序、构建信息、`.env.example`、许可证和公开文档，不包含 `.env`、商店数据、密钥、日志或演示 Token。`build-info.txt` 含 Go 构建信息和源码提交号，`third-party/` 与 `THIRD_PARTY_NOTICES.md` 保留依赖版权和授权说明。SHA-256 用于完整性校验，不是独立的代码签名或供应链证明。

```bash
./dujiao-next-telegram-bot --version
# 0.4.0
```

程序只读取进程环境变量，不自动加载 `.env`。Docker Compose 可直接读取 `.env`；运行二进制时请由服务管理器或受控环境文件注入变量，不要把令牌写在命令行或提交到仓库。Linux 二进制使用静态 Go 构建，但支付及 Telegram HTTPS 仍要求系统有可用的 CA 根证书。跨平台下载包经过交叉编译，不代表每种系统均完成原生运行测试。

## 新部署

1. 固定源码版本 `v0.4.0`，复制 `.env.example` 为 `.env` 并填入真实商店 Channel API 配置。
2. 若使用提现或公告群发，建议启用 Redis：生成一次独立的 `BOT_SESSION_ENCRYPTION_KEY`，稳定保存并备份。
3. 管理员白名单填私人 Telegram 数字 ID，先与 Bot 私聊；`/whoami` 核对 ID，`/admin` 测试管理员通知。
4. 启动 `docker compose -f compose.yml -f compose.redis.yml up -d --build --wait`。
5. 确认 `/healthz` 为 200、运行版本为 0.4.0，按商店测试环境验证购物、充值与签名回调。不要用真实资金做压力测试。

```bash
docker compose -f compose.yml -f compose.redis.yml exec -T bot /usr/local/bin/dujiao-bot --version
curl --fail http://127.0.0.1:8444/healthz
```

`BOT_BROADCAST_INTERVAL=1s`、`BOT_BROADCAST_MAX_RECIPIENTS=1000` 是保守默认值；仅显式 `/subscribe` 的用户进入公告受众。发布/升级不会自动订阅现有买家，不会自动执行群发、申请提现或打款。

## 从现有版本升级

先确认当前工作区干净，保存当前提交/镜像标识。未提交的自定义代码需单独处理，不要直接覆盖。保持同一 Compose 项目名、配置路径和数据卷；本文命令应在已建立的生产项目目录执行，不用于演示栈。

1. 核对未决提现与支付结果，停止剩余公告任务或记录其进度。Redis 中旧的群发任务重启后会暂停，由原创建者核对后手动继续。
2. 停止 **Bot 服务**，使用自己的备份设施保存 `.env`、独立加密密钥、商店数据、Redis 一致性快照/AOF 和审计卷。确认备份可恢复；不要把备份提交到 GitHub。备份 Redis 卷时使用一致性快照或先停止 Redis，不要在写入期间简单复制文件来假定一致性。
3. 获取标签并固定源码到 `v0.4.0`。保持 `DUJIAO_BASE_URL`、`DUJIAO_CHANNEL_KEY` 和 Redis 加密密钥不变；变更它们可能改变命名空间或无法解密旧状态。
4. 重建并启动 **Bot 服务**，不重建商店、不删除 Redis 或审计卷：

   ```bash
   docker compose -f compose.yml -f compose.redis.yml stop bot
   git fetch origin tag v0.4.0
   git switch --detach v0.4.0
   docker compose -f compose.yml -f compose.redis.yml up -d --no-deps --build --wait bot
   ```

5. 核对版本、健康、签名回调、管理员权限及历史记录。启用 Redis 前使用内存模式的会话不会自动迁移，必须先人工核对未决资金请求；不把“新会话为空”视为上次操作失败。

不使用 Redis 的部署去掉 `-f compose.redis.yml`。演示项目必须使用 `compose.demo.yml` 和原项目名，并只更新 `bot`；重建 `demo-store` 会丢失其内存中的订单与演示余额。不要运行 `down -v` 或改项目名创建空数据卷来完成升级。

## 回滚与故障处理

回滚前先停止 Bot，保存当前数据副本并核对已经发出的资金请求。优先回滚到能读取当前 Redis 保护状态的已知兼容提交/镜像；不能因为旧程序可启动，就认定它仍执行新增的提现保护和公告语义。v0.3 及更旧版本不提供这套 Redis 防重能力，不应直接用空内存状态继续处理提现。无法确认兼容性时保持 Bot 停止，先人工核对，而非清空 Redis 或换密钥强行启动。

恢复旧 Redis 快照会同时回滚发送游标、退订和资金保护，可能重新处理已经发出的操作；不能仅为回滚程序而回滚业务状态。回滚后仍须核对商店账本和 Telegram 发送结果。不删除数据卷，不重放结果不明请求。

| 现象 | 处理 |
| --- | --- |
| `/healthz` 为 503 | 检查 Redis 连接、持久卷与数据集标识；恢复原实例，不回退到内存绕过保护 |
| 加密密钥/状态不兼容 | 恢复原密钥和匹配数据副本；不清空数据来消除错误 |
| 提现结果不明 | 查看商店提现记录并联系商家核对；仅用户明确核对后解除保护，不自动重试 |
| 公告暂停或 `prepared` | 原创建者用 `/broadcast_status` 核对后继续尚未领取者，或停止；未决领取标记可能需等待约一分钟 |
| 管理员收不到摘要 | 核对数字 ID、是否已私聊/屏蔽 Bot，以及 `admin.delivery` 审计；内存通知队列不保证崩溃恢复 |
| Telegram 429 | 等待 `retry_after`，不通过重建任务绕过，不提高额度进行真实群发压测 |

## 维护者验收与打包

```bash
make verify      # 格式、依赖、静态检查、三轮竞态测试与构建
make acceptance  # 固定隔离 Mock 栈，含真实 Redis AOF/故障恢复、限流压测和审计卷检查
make release     # 当前稳定版本的四种平台下载包与 SHA-256 清单
```

`make acceptance` 仅操作 `dujiao-bot-integration` 项目和 `compose.test.yml` 的假凭证，保留容器及卷；不操作演示 Bot 或生产商店。主机需有 Docker Compose、Go、curl，且本机 18444/16379 端口由此测试栈使用。验收会短暂停止测试 Redis、重启测试 Bot，不要把测试端口映射到生产服务。`make release` 要求工作区干净且已经提交，写入全新 `dist/release/v<版本>`，已有目录时拒绝覆盖。

发布时以同一提交的 Go/Docker CI 成功为门槛，固定 `v0.4.0` 标签，附上下载包和 `checksums.txt`。版本号可查询、公开文档不含密钥、镜像/二进制构建成功，不等于真实支付渠道已被本项目代为验收；兼容性和商家实际支付闭环仍需在商家测试环境确认。
