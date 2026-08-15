# chatgpt-plus-price-monitor

监控 [priceai.cc](https://priceai.cc/products/chatgpt-plus) 上 ChatGPT Plus 的报价，
当**最便宜的 N 个的均价**低于阈值时通过 Telegram 通知。

**零第三方依赖**，只用 Go 标准库。

## 编译

```sh
go build -o chatgpt-plus-price-monitor .
```

## 快速开始

先看看现在什么价（不发通知、不写状态）：

```sh
./chatgpt-plus-price-monitor --dry-run --verbose
```

```
最便宜的 5 个均价 104.75 元（最低 100.94 / 最高 108.15），阈值 10.00 -> 未达成
  1. 100.94 元 | Ai小卖部 | 【自营】gpt plus （upi） 未接码 | 质保一个月...
  2. 101.97 元 | Ai俱乐部 | GPT plus （8.10号菲区正价开通的Gmail成品号）
  ...
```

**先用它确认 `--threshold` 定在合理位置**，再配通知。

然后配置 Telegram：

1. 找 [@BotFather](https://t.me/BotFather) 发 `/newbot`，拿到形如 `123456:ABC-DEF...` 的 token；
2. 给你的 bot 发一条消息，然后访问
   `https://api.telegram.org/bot<TOKEN>/getUpdates`，从返回里找到 `chat.id`；
3. 用环境变量传入（**不要写在命令行参数里**，`ps` 能看到别人的进程参数）：

```sh
export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
export TELEGRAM_CHAT_ID=123456789
./chatgpt-plus-price-monitor --interval 30m --top 5 --threshold 10
```

## 选项

| 选项 | 默认值 | 说明 |
| --- | --- | --- |
| `--top` | `5` | 取最便宜的 N 个报价计算均价 |
| `--threshold` | `10` | 阈值（元），均价 ≤ 该值时通知 |
| `--interval` | `0` | 轮询间隔（如 `30m`）；为 0 表示只检查一次就退出，交给 cron |
| `--timeout` | `30s` | 单次 HTTP 请求超时 |
| `--state` | `state.json` | 状态文件路径，用于通知去重 |
| `--cooldown` | `24h` | 价格持续低于阈值时的重复提醒间隔；`0` = 只在跌破那一刻提醒一次 |
| `--notify-recover` | `true` | 价格回升到阈值之上时也发一条 |
| `--fail-threshold` | `3` | 连续抓取失败 N 次后发告警；`0` = 关闭 |
| `--telegram-token` | `$TELEGRAM_BOT_TOKEN` | Bot Token |
| `--telegram-chat` | `$TELEGRAM_CHAT_ID` | Chat ID |
| `--telegram-api` | `https://api.telegram.org` | API 地址，国内直连不通时可指向自建 Bot API 或反代 |
| `--dry-run` | `false` | 只抓取并打印，不发通知、不写状态 |
| `--verbose` | `false` | 打印每条报价的店铺和标题 |

## 数据来源

直接调页面背后的接口，不解析 HTML：

```
GET https://priceai.cc/api/products/chatgpt-plus/offers?limit=<top>&offset=0
```

这个接口**默认按价格升序返回**，所以 `offset=0&limit=N` 拿到的就是最便宜的 N 条，
也正是页面上显示的前 N 条，本地不需要再排序。

每条报价里用到了 `price`、`currency`、`sourceStoreName`、`sourceTitle`、`url`，
通知里会把价格做成可点的链接，收到就能直接跳去下单。

## 几个设计上的说明

**为什么需要 Chat ID？**
Bot Token 只能证明"你是这个 bot"，还得告诉它把消息发给谁。两个都要。

**为什么有状态文件？**
如果每轮检查都推送，价格只要在阈值下待着，你半小时就会收到一条，很快就会把通知静音。
所以只在**状态发生变化**时通知：

- 均价从阈值之上跌到之下 → 发「降价提醒」
- 持续低于阈值 → 静默，直到超过 `--cooldown`（默认 24 小时）才再提醒一次
- 均价回升到阈值之上 → 发「价格回升」，之后恢复静默

删掉 `state.json` 就能重置状态。

**监控自己坏了怎么办？**
接口改版、被限流、网络不通，这些都会让抓取失败。挂了 cron 又不看日志的话，
监控会静默死掉——你以为它在盯着，其实早就不工作了。

所以连续失败 `--fail-threshold` 次（默认 3 次）之后会推一条告警，带上原始错误：

```
⚠️ 价格监控异常

已连续 3 次抓取失败，监控可能已经失效。

最后一次错误：
接口只返回了 0 条报价，少于需要的 5 条
```

一轮故障只告警一次，不会每次失败都推。抓取恢复后补一条 ✅ 恢复通知，
计数清零。如果告警本身也发不出去（比如整个网络都断了），不会记成"已告警"，
下一轮会重试。

失败计数存在状态文件里而不是内存里，所以 cron 单次模式（每轮都是新进程）
一样能累计。

**接口返回条数不够时会直接报错。**
如果接口改版或被限流只返回了 2 条，那这 2 条的均价并不是你想监控的东西，
却很可能凑巧低于阈值而误报。所以条数少于 `--top` 时宁可报错退出（exit code 1）。
币种不是 CNY 也同理报错——阈值是按人民币比的。

**关于 Telegram 在国内的连通性：**
`api.telegram.org` 在中国大陆无法直连。要么让程序走代理（Go 会自动读取环境变量）：

```sh
export HTTPS_PROXY=http://127.0.0.1:7890
```

要么用 `--telegram-api` 指向你自己的反代或
[自建 Bot API Server](https://github.com/tdlib/telegram-bot-api)。

发消息用的是 `POST /bot<token>/sendMessage`，参数放在 JSON body 里而不是 URL query。
两种都能用，但 POST 更好：消息正文不进 URL，省掉中文和 emoji 的转义问题，
也不会让 token 和正文出现在中间代理的访问日志里。

## 定时运行

**crontab**（每 30 分钟，注意用绝对路径）：

```cron
*/30 * * * * TELEGRAM_BOT_TOKEN=xxx TELEGRAM_CHAT_ID=yyy /opt/monitor/chatgpt-plus-price-monitor --state /opt/monitor/state.json >> /var/log/price-monitor.log 2>&1
```

**systemd**（常驻，凭据放在 `/etc/price-monitor.env`，权限设成 600）：

```ini
[Unit]
Description=ChatGPT Plus price monitor
After=network-online.target

[Service]
EnvironmentFile=/etc/price-monitor.env
WorkingDirectory=/opt/monitor
ExecStart=/opt/monitor/chatgpt-plus-price-monitor --interval 30m --top 5 --threshold 10
Restart=always
RestartSec=60

[Install]
WantedBy=multi-user.target
```

## 测试

```sh
go test ./...
```

接口解析用的是真实抓取的响应（`internal/priceai/testdata/offers.json`）。
另外覆盖了通知去重的状态机、`checkOnce` 的完整通知链路（对着假的 Telegram 服务器），
以及 Telegram 客户端的成功与失败分支。
