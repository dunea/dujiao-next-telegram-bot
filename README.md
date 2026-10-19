# Dujiao Next Telegram Bot Community

[![CI](https://github.com/dunea/dujiao-next-telegram-bot/actions/workflows/ci.yml/badge.svg)](https://github.com/dunea/dujiao-next-telegram-bot/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

一个免费、开源的 Dujiao-Next Telegram 商店机器人。项目使用 Go 开发，通过 Dujiao-Next Channel API 读取商店配置、商品、订单、钱包和支付信息。

> 这是社区项目，不是 Dujiao-Next 官方收费机器人的替代授权，也不隶属于 Telegram。

## 已实现

- Telegram 用户自动解析和创建，支持 `/start <邀请码>`
- 简体中文、繁体中文、英文切换
- 商品分类、分页列表、商品详情、SKU 与购买数量选择
- 起购、限购和可见库存数量校验
- 订单预览与创建，人工交付商品可在私聊中逐项填写购买资料
- 自定义表单支持文本、多行文本、邮箱、电话、数字、单选和多选，校验必填、格式、长度与范围
- 填写中支持返回上一项、跳过选填项、取消，预览后可修改资料
- 订单预览支持输入、更换和移除优惠码，展示优惠明细，正式下单携带相同优惠码
- 余额或在线支付渠道选择、支付链接与二维码展示
- 待支付订单自动轮询，成功后清理二维码并展示交付结果
- 我的订单、状态筛选、订单详情、取消待支付订单与交付内容展示
- 钱包余额、钱包充值与分页交易流水
- 充值预设金额、自定义金额、渠道费率/固定费用参考，以及到账金额、渠道手续费和实付金额分开展示
- 礼品卡兑换，展示到账金额、最新余额和兑换流水，支持无效、过期、停用与已兑换提示
- 推广中心、开通推广、专属 Bot 链接分享、访问及转化数据、佣金和提现记录分页查询
- 推广链接进入时记录访问，订单预览和创建携带一致访客标识，由 Dujiao-Next 保存和校验归因
- 订单支付、交付、钱包充值成功的签名回调通知
- Dujiao-Next 远端机器人配置定时刷新与心跳
- 长轮询运行，不要求 Telegram Webhook 或公网 HTTPS
- Docker Compose 部署和 `/healthz` 健康检查

暂不支持机器人内提交提现申请、分销站管理和后台群发。这些可以在后续版本逐步补充。

后续迭代顺序和完成标准见 [ROADMAP.md](ROADMAP.md)。

当前开发版本：`v0.4.0-dev`；已完成的功能版本为 `v0.3.0`。变更记录见 [CHANGELOG.md](CHANGELOG.md)。

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
- `/redeem` 兑换礼品卡到钱包余额
- `/affiliate` 打开推广中心
- `/language` 切换语言
- `/help` 查看帮助
- `/cancel` 取消邀请码输入等当前操作

填写购买资料时，回复当前问题输入文本，单选、多选使用消息下方按钮。可发送 `/back` 返回上一项、`/skip` 跳过选填项或 `/cancel` 取消；填写结束后会展示订单和资料摘要供确认。表单仅在私聊中使用，草稿 30 分钟内有效，重启后需重新填写。

确认订单前可点击“输入优惠码”，在私聊中回复优惠码。优惠券规则和金额以 Dujiao-Next 预览结果为准；可更换、移除优惠码，或发送 `/back` 保留原优惠码返回预览。无效优惠码不会清除已填的购买资料；提交前若优惠后金额变化，会展示新预览并要求再次确认。订单草稿和优惠码有效期为 30 分钟，重启后需重新选择。

在私聊中打开“钱包 → 礼品卡兑换”，或发送 `/redeem`，回复当前提示输入兑换码。可发送 `/back` 返回钱包、`/cancel` 取消；输入提示 10 分钟内有效。兑换结果来自 Dujiao-Next 的 `/api/v1/channel/wallet/gift-card/redeem` 接口，成功后显示到账金额和余额，流水类型为 `gift_card_redeem`。网络异常或结果不明确时不会自动重试，请先查看余额和流水。兑换码不写入机器人本地会话或日志。

发送 `/recharge` 或在钱包中点击充值，可选择 `10 / 50 / 100 / 200 / 500` 的预设金额，也可输入自定义金额（大于 0、最多两位小数、上限 1,000,000；币种来自商店钱包）。点击“自定义金额”后请回复当前提示；发送 `/cancel` 取消。充值选择仅限私聊，10 分钟内有效，退出、重新选择或提交后旧按钮失效，同一选择不会重复创建充值单。网络异常时不会自动重试，请先核对钱包、流水和支付账户。

支付渠道页展示费率、固定费用和渠道手续费参考，采用十进制定点计算和四舍五入。Channel API 未提供“用户承担/商家承担手续费”配置，所以参考费用不是最终报价。创建充值单后，到账金额、渠道手续费与应付/实付金额分别展示；支付金额直接使用商店返回值，不重复加手续费。同币种且渠道费用由商家承担时明确注明未加收；发生换汇时保留钱包和支付币种，不混算不同币种金额。

### 推广中心（v0.4 第一批）

私聊发送 `/affiliate` 或点击首页“推广中心”。未开通时需要点击“开通推广”；已开通后展示专属 Bot 链接、推广访问、有效订单、转化率、待确认/可用/已提现佣金及最低提现金额。支持分享链接、刷新和分页查询佣金、提现记录；收款账号默认脱敏。本批只读展示提现记录，不会提交提现或触发打款，申请请在商店完成或联系客服。

链接格式为 `https://t.me/<实际机器人用户名>?start=aff_<推广码>`，其中 `aff_` 是保留的推广前缀，不能作为普通注册邀请码前缀。普通 `/start <邀请码>` 行为不变。推广码不是邀请码：要求邀请注册的商店仍需另外输入有效邀请码。

访问推广链接时调用 `/affiliate/click`，传入固定访客标识 `telegram:<Telegram 用户 ID>`；普通和人工交付商品的订单预览、最终下单均发送相同的 `affiliate_visitor_key`。不把推广码持久化在 Bot 会话，也不在所有订单里重复提交原始推广码。归因、去重、有效期、自购、退款、佣金结算和提现审核均由商店处理；目前核对的 Dujiao-Next 实现采用最近 30 天有效访问归因、同访客同落地页 10 分钟去重。Bot 重启或取消购买不会主动清除商店保存的归因，实际期限以商店版本为准。

推广 API 未启用、商店版本不支持或账号不可用时会提示原因。记录推广访问失败会提示用户重开链接，但不会阻止打开商店。运行需要提供 `/api/v1/channel/affiliate/*` 接口的商店版本；旧版本的已有购物功能不依赖这些推广接口。

如使用商店自定义首页菜单，可使用 `affiliate` 或 `my_affiliate` 菜单项键添加推广中心入口；无论是否配置入口，命令 `/affiliate` 都可使用。

## 安全说明

- Channel Secret 和 Bot Token 只通过环境变量传入，不要提交 `.env`。
- 所有 Dujiao-Next Channel API 请求使用协议要求的 HMAC-SHA256 签名。
- 所有内部回调会校验 Channel Key、签名及五分钟时间窗口。
- 用户、订单和支付数据以 Dujiao-Next 为准；本地内存保存语言、临时交互状态及尚未提交的表单草稿。取消或提交后清除草稿，过期后读取时清除，重启后不保留。

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

“人工配置服务”演示域名、邮箱、区域单选、配置多选和可跳过的备注。可使用 `demo.example.com`、`demo@example.com` 等虚构资料测试。订单付款后进入“处理中”，不会生成自动发货授权码。

演示优惠码：`SAVE10` 打九折；`LESS5` 满 20 CNY 减 5 CNY；`EXPIRED` 用于测试无效或过期提示。以上规则仅适用于演示商城，真实商店使用后台配置的优惠券。

演示礼品卡：`GIFT50` 兑换 50 CNY、`GIFT100` 兑换 100 CNY；`GIFT-EXPIRED`、`GIFT-DISABLED`、`GIFT-USED` 分别测试过期、停用和已兑换提示。每张有效演示卡仅能兑换一次，重启演示商城后重置，不会产生真实资金。

演示充值有免手续费、用户承担手续费、商家承担手续费三条渠道，均模拟即时到账，不收取真实资金。以充值 `100 CNY` 为例：免手续费实付 `100.00`；用户承担 `2.50% + 1.00 CNY` 时渠道手续费 `3.50`、实付 `103.50`；商家承担时仍显示渠道手续费 `3.50`，但实付为 `100.00`。三种渠道的钱包到账金额均为 `100.00 CNY`。重建演示商城会重置余额、订单和演示卡状态。

演示推广：先在推广者账号发送 `/affiliate` 并开通，将生成的链接发给另一个 Telegram 账号。购买者通过链接打开商店并完成演示付款后，推广者刷新可看到访问、有效订单和待确认佣金。演示佣金按优惠后实付本金的 `10%` 四舍五入计算，重复付款不会重复产生佣金，自购不计佣金；这仅用于演示，不代表真实商店规则，也不提供真实结算或打款。新开通时明细为空，重建演示商店会重置推广档案和演示佣金。

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
