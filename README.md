# chatgpt-plus-price-monitor

监控 [priceai.cc](https://priceai.cc/products/chatgpt-plus) 上 ChatGPT Plus 的报价，
当**最便宜的 N 个的均价**低于阈值时通过 Telegram 通知。

零第三方依赖，只用 Go 标准库。

## 用法

```sh
go build -o chatgpt-plus-price-monitor .
```

先看看现在什么价。**不设环境变量时只打印日志，不发通知**：

```sh
./chatgpt-plus-price-monitor --once -v
```

```
未设置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID，只打印结果，不发送通知
最便宜的 5 个均价 104.75 元（最低 100.94 / 最高 108.15），阈值 10.00 -> 未达成
  1. 100.94 元 | Ai小卖部 | 【自营】gpt plus （upi） 未接码 | 质保一个月...
  2. 101.97 元 | Ai俱乐部 | GPT plus （8.10号菲区正价开通的Gmail成品号）
  ...
```

用它确认 `--threshold` 定在合理位置，再配通知：

1. 找 [@BotFather](https://t.me/BotFather) 发 `/newbot`，拿到形如 `123456:ABC-DEF...` 的 token；
2. 给你的 bot 发一条消息，然后访问
   `https://api.telegram.org/bot<TOKEN>/getUpdates`，从返回里找到 `chat.id`；
3. 两个都设置好，就会开始发通知：

```sh
export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
export TELEGRAM_CHAT_ID=123456789
./chatgpt-plus-price-monitor -i 30m -n 5 -t 10
```

凭据只从环境变量读，没做成命令行选项：写在命令行上同机器的其他人 `ps` 就能看到。

## 选项

| 选项 | 默认值 | 说明 |
| --- | --- | --- |
| `-n, --top` | `5` | 取最便宜的 N 个报价计算均价 |
| `-t, --threshold` | `10` | 均价低于该价格（元）时通知 |
| `-i, --interval` | `30m` | 轮询间隔 |
| `--once` | | 只检查一次就退出 |
| `--cooldown` | `24h` | 持续低于阈值时的重复提醒间隔，`0` 表示只提醒一次 |
| `--no-rebound` | | 价格回升到阈值之上时不通知 |
| `--fail-threshold` | `3` | 连续抓取失败 N 次后告警，`0` 表示关闭 |
| `--timeout` | `30s` | 单次 HTTP 请求超时 |
| `-v, --verbose` | | 打印每条报价的店铺和标题 |

## 数据来源

直接调页面背后的接口，不解析 HTML：

```
GET https://priceai.cc/api/products/chatgpt-plus/offers?limit=<top>&offset=0
```

这个接口**默认按价格升序返回**，所以 `offset=0&limit=N` 拿到的就是最便宜的 N 条，
也正是页面上显示的前 N 条，本地不需要再排序。

用到的字段是 `price`、`currency`、`sourceStoreName`、`sourceTitle`、`url`。
通知里会把价格做成可点的链接，收到就能直接跳去下单。

接口返回条数少于 `--top`、或币种不是 CNY 时直接报错退出（exit code 1）。
不完整或不可比的样本算出来的均价很可能凑巧低于阈值而误报，宁可报错。

## 通知时机

只在**状态发生变化**时通知，而不是每轮都推：

- 均价从阈值之上跌到之下 → 「降价提醒」
- 持续低于阈值 → 静默，直到超过 `--cooldown`（默认 24 小时）才再提醒一次
- 均价回升到阈值之上 → 「价格回升」，之后恢复静默
- 连续抓取失败 `--fail-threshold` 次 → 「监控异常」，带上原始错误
- 抓取恢复 → 「监控已恢复」

抓取失败告警是为了防止监控静默死掉：接口改版、被限流、网络不通都会让抓取失败，
日志没人看的话，你以为它在盯着，其实早就不工作了。
一轮故障只告警一次；如果告警本身也发不出去，不会记成"已告警"，下一轮会重试。

去重状态只存在内存里，所以要常驻运行才有意义。重启会重新武装：
如果重启时价格仍低于阈值，会再收到一条降价提醒。
`--once` 每次都是全新状态，适合手动看一眼当前价格，不适合挂 cron。
