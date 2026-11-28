# 币安扩展

[English](README.md)

在 Persona 中查询你的币安账户，在手机逐单确认后按自定义风控护栏下单个现货、U 本位或币本位合约订单与撤单；也支持开启隔离的纸面模拟盘，在不向交易所发送任何写请求的情况下验证策略与流程。

## 功能概览

只读工具（编程 Agent 也可用）：`binance_overview`、`binance_balances`、`binance_positions`、`binance_open_orders`、`binance_order`、`binance_trades`、`binance_income`、`binance_transfers`（仅充值与提现历史记录）、`binance_price`、`binance_limits`、`binance_status`。

实盘写操作（仅助手可用，全部通过 manifest 的 `confirm_always` 走手机确认）：`binance_spot_order`、`binance_spot_cancel`、`binance_usdm_order`、`binance_usdm_cancel`、`binance_coinm_order`、`binance_coinm_cancel`。支持 `LIMIT` 和 `MARKET` 订单；合约支持单向持仓的 `reduce_only` 与双向持仓的 `position_side`。

明确不提供且代码层面不可达的能力：提现、任何划转、借贷、修改杠杆或保证金模式、条件单（止损、OCO、算法单）、批量单与一键全撤。客户端仅允许调用 [endpoints.go](endpoints.go) 白名单表内的接口；`endpoints_test.go` 会拦截任何涉及出金、借贷或改杠杆/保证金的路径。

收件箱事件（带兜底分级，紧急事件推送提醒）：`liq_risk`、`position_change`（非 Persona 下单导致的仓位变化）、`order_filled`、`order_rejected`、`price_move`、`deposit`、`withdrawal`、`key_problem`。

## 风控护栏

每笔订单在生成确认卡片时检查一次（使用缓存行情与交易规则，不阻塞网络），在手机确认提交前再拉取实时价格复检一次：

| 设置项 | 默认值 | 说明 |
| --- | --- | --- |
| `proxy_url` | 空 | 可选，留空时直接经由 Hub 出口访问币安 |
| `trading_enabled` | 关闭（`false`） | 实盘与纸面盘共用的下单总开关；关闭时拒绝一切下单与撤单，查询照常 |
| `paper_trading` | 关闭（`false`） | 开启后切换至独立纸面模拟盘，不向交易所发单 |
| `markets` | `["spot"]` | 允许下单的市场（`spot` / `usdm` / `coinm`） |
| `symbol_whitelist` | 空（`[]`） | 允许下单的币对白名单；留空时拒绝所有下单（撤单不受限） |
| `max_order_usdt` | `200` | 单笔估算名义金额上限（USDT） |
| `max_daily_usdt` | `1000` | 每日累计上限（按 `timezone` 00:00 清零；只减仓和撤单不计入） |
| `max_orders_per_day` | `20` | 每日下单笔数上限 |
| `max_leverage` | `5` | 合约最大允许杠杆（扩展不会改杠杆，超限时拒绝开仓） |
| `limit_deviation_pct` | `5` | 限价相对现价的最大允许偏离百分比（%） |
| `market_orders` | 开启（`true`） | 是否允许市价单 |
| `market_slippage_pct` | `1` | 市价单提交前现价相对卡片报价的最大滑点保护（%） |

每张确认卡片最多产生一笔订单：`newClientOrderId` 由 Hub 的 `request_id` 派生，重复提交只返回首次结果。被币安明确拒绝的订单会退回额度；若发送时遇断连、超时或 5xx 等结果不明错误，扩展绝不自动重发，而是按 client id 反查，仅在币安连续两次确认「订单不存在」后才退回额度，否则标记为未知、保留额度占用并推送提醒你核对。跨日未成交挂单继续占用次日额度，市价单按当前价加滑点护栏的最坏情况扣减额度。

## 纸面模拟盘

同时开启 `paper_trading` 和 `trading_enabled` 后，写操作目录会切换为仅包含 `binance_paper_spot_order`、`binance_paper_usdm_order` 和 `binance_paper_coinm_order`（纸面模式下拒绝实盘下单与撤单，切回实盘后拒绝旧纸面动作）：

- **零交易所写调用**：纸面单会拉取实时行情、完整执行所有风控护栏（白名单、单笔/日额度、限价偏离、市价滑点、杠杆与持仓检查），并将模拟结果与额度写入独立的 `paper-ledger.json`，绝不调用币安下单或撤单接口。
- **现货免 Key 模拟**：现货纸面模拟仅依赖公开行情，不配置 API Key 也可运行；合约纸面模拟仍需配置合规 API Key 以读取持仓模式、杠杆与当前持仓。
- **模拟规则与边界**：市价单按提交前获取的实时参考价记录模拟成交；限价单仅当参考价当场达到委托价时按限价记录模拟成交，否则记为模拟未成交（`EXPIRED`）且占用当日额度。纸面盘不维护挂单簿、不提供纸面撤单，也不模拟手续费、盘口深度、部分成交或虚拟账户余额；实盘余额和挂单查询工具仍返回交易所真实数据。

## 安装与配置

1. 在 Hub 所在机器生成 Ed25519 密钥对：`openssl genpkey -algorithm ed25519 -out binance.pem` 与 `openssl pkey -in binance.pem -pubout -out binance.pub.pem`。
2. 在币安 API 管理中用公钥创建「自生成」API Key。开启**读取**与所需交易权限（**现货及杠杆交易**和/或**合约**）；务必关闭提现、内部划转、万向划转、杠杆借贷、期权和统一账户权限，并选择「仅限受信任 IP」绑定 Hub 出口公网 IP（或下方代理出口 IP）。
3. 在 **设置 › 扩展 › 币安** 中填写 API Key 和 Ed25519 私钥（PEM）。保存前会先向币安校验权限，拒绝开启提现/划转、未绑定 IP 白名单或缺少读写权限的 Key。
4. 如需通过 Hub 本机代理访问币安，可选填写 `proxy_url`（默认留空），格式为 `http://host.persona.internal:<端口>`（支持 HTTP CONNECT）；留空则直接走 Hub 出口。
5. 开启 `trading_enabled` 并在 `symbol_whitelist` 中添加允许交易的币对（建议先开启 `paper_trading` 演练完整流程，再切回实盘）。

API Key 与私钥仅通过 Hub 加密密钥通道保存，不会写入日志，也不会发送到币安以外的任何地址（Key 仅放入 `X-MBX-APIKEY` 请求头，签名仅用于请求参数）。

## 本地验证

```sh
go -C extensions/binance test -race ./...
scripts/package.sh binance
```

所有单测均使用本地合成的模拟币安服务与每次运行临时生成的密钥对，不需要真实账户。
