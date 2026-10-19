# Dujiao Next Telegram Bot Community

[![CI](https://github.com/dunea/dujiao-next-telegram-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/dunea/dujiao-next-telegram-bot/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

一个免费、开源的 Dujiao-Next Telegram 商店机器人。项目使用 Go 开发，通过 Dujiao-Next Channel API 读取商店配置、商品、订单、钱包和支付信息。

> 这是社区项目，不是 Dujiao-Next 官方收费机器人的替代授权，也不隶属于 Telegram。

## 已实现

- Telegram 用户自动解析和创建，支持 `/start <邀请码>`
- 简体中文、繁体中文、英文切换
- 商品分类、分页列表、商品详情和 SKU 选择
- 无自定义表单商品的订单预览与创建
- 余额或在线支付渠道选择、支付链接展示
- 我的订单、订单详情、取消待支付订单与交付内容展示
- 钱包余额与钱包充值
- 订单支付、交付、钱包充值成功的签名回调通知
- Dujiao-Next 远端机器人配置定时刷新与心跳
- 长轮询运行，不要求 Telegram Webhook 或公网 HTTPS
- Docker Compose 部署和 `/healthz` 健康检查

暂不支持需要填写自定义表单的商品、优惠券输入、礼品卡、分销和后台群发。这些可以在后续版本逐步补充。

后续迭代顺序和完成标准见 [ROADMAP.md](ROADMAP.md)。

## 环境要求

- Dujiao-Next `v1.5.0` 或兼容 Channel API 的版本
- Go 1.25+（本地构建）或 Docker Compose
- 一个通过 [@BotFather](https://t.me/BotFather) 创建的 Telegram Bot

项目使用仍在维护的 [`github.com/go-telegram/bot`](https://github.com/go-telegram/bot)，没有使用多年未发布新版本的旧 `go-telegram-bot-api/v5`。

## 配置

1. 在 Dujiao-Next 后台配置 Telegram Bot 服务，取得 Channel Key 和 Channel Secret，并填入 Bot Token。
2. 复制环境变量模板：

   ```bash
   cp .env.example .env
   ```

3. 编辑 `.env`：

   ```dotenv
   DUJIAO_BASE_URL=https://shop.example.com
   DUJIAO_CHANNEL_KEY=your-channel-key
   DUJIAO_CHANNEL_SECRET=your-channel-secret

   # 可不填；为空时从 Dujiao-Next 的 Telegram 服务配置读取
   TELEGRAM_BOT_TOKEN=
   ```

4. 如果需要支付/交付主动通知，将 Dujiao-Next Telegram 服务的回调地址设置为机器人可访问的地址，例如：

   ```text
   https://bot.example.com/internal/order-paid
   ```

   Dujiao-Next 会自动将路径替换成实际事件路径。机器人同时接收：

   - `/internal/order-paid`
   - `/internal/order-fulfilled`
   - `/internal/wallet-recharge-succeeded`

   回调必须能从 Dujiao-Next 服务器访问。如果两者在同一个 Docker 网络，可以直接使用内网服务地址；Telegram 长轮询本身不需要这个公网入口。

## 启动

Docker Compose：

```bash
docker compose up -d --build
docker compose logs -f bot
```

本地运行：

```bash
go run ./cmd/bot
```

验证：

```bash
curl http://127.0.0.1:8444/healthz
go test ./...
```

## 常用命令

- `/start` 打开商店并初始化账号
- `/products` 浏览商品
- `/orders` 查看订单
- `/wallet` 查看余额
- `/recharge` 发起钱包充值
- `/language` 切换语言
- `/help` 查看帮助
- `/cancel` 取消邀请码输入等当前操作

## 安全说明

- Channel Secret 和 Bot Token 只通过环境变量传入，不要提交 `.env`。
- 所有 Dujiao-Next Channel API 请求使用协议要求的 HMAC-SHA256 签名。
- 所有内部回调会校验 Channel Key、签名及五分钟时间窗口。
- 机器人不会在本地保存用户、订单或支付数据；内存只保存语言和临时交互状态，重启后自动恢复默认语言。

## 开发

```bash
make test
make lint
make build
```

### 本地 Telegram 演示商店

在 `.env` 中只填写 `TELEGRAM_BOT_TOKEN`，即可用内置演示商品启动真实 Telegram Bot；演示充值会即时到账，不会产生真实付款：

```bash
make demo-up
docker compose -f compose.demo.yml logs -f bot
```

演示栈的健康端点为 `http://127.0.0.1:19444/healthz`。测试结束后运行：

```bash
make demo-down
```

### Docker 集成测试与压测

不需要真实密钥即可启动 Mock Dujiao-Next、Mock Telegram API 和真实机器人镜像：

```bash
make integration-up
docker compose -f compose.test.yml ps
```

测试栈将机器人健康端点映射到 `http://127.0.0.1:18444/healthz`。运行内置压测：

```bash
make loadtest
```

也可以调整请求量和并发数：

```bash
go run ./cmd/loadtest -mode health -n 5000 -c 100
go run ./cmd/loadtest -mode callback -n 2000 -c 50
```

`callback` 模式会覆盖“Dujiao 签名回调 → 验签 → 机器人通知 → Telegram API”的完整链路。结束后清理测试容器：

```bash
make integration-down
```

`TELEGRAM_API_URL` 仅用于自建 Telegram Bot API 或本地测试，正常部署时保持为空。

目录说明：

```text
cmd/bot/             启动与生命周期管理
cmd/loadtest/        本地 HTTP/回调压测工具
cmd/mock-api/        Docker 集成测试 Mock API
internal/callback/   Dujiao-Next 主动回调验签
internal/config/     环境变量配置
internal/dujiao/     Channel API 客户端与签名
internal/session/    临时会话状态
internal/telegram/   Telegram 交互和业务流程
```

## License

[MIT](LICENSE)
