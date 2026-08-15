# chatgpt-plus-price-monitor

监控 [priceai.cc](https://priceai.cc/products/chatgpt-plus) 上 ChatGPT Plus 的挂单价格，
当**前 N 个的均价**低于阈值时通过 Telegram 通知。

## 编译

```sh
go build -o chatgpt-plus-price-monitor .
```

需要 Go 1.23+，只依赖 `goquery` 一个第三方库。

## 快速开始

**第一步，先确认能正确抓到价格**（不发通知，不写状态）：

```sh
./chatgpt-plus-price-monitor -dry-run -verbose
```

输出类似：

```
提取方式=embedded-json 共 24 个价格: 8.80, 9.00, 9.50, 10.00, 11.20, ...
前 5 个均价 9.70 元（最低 8.80 / 最高 11.20），阈值 10.00 -> 已达成
```

核对这几个数字和网页上看到的是否一致。**一致了再配通知**，否则容易收到假警报。

如果抓不到或者对不上，见下面的「抓不到价格怎么办」。

**第二步，配置 Telegram：**

1. 找 [@BotFather](https://t.me/BotFather) 发 `/newbot`，拿到形如 `123456:ABC-DEF...` 的 token；
2. 给你的 bot 发一条消息，然后访问
   `https://api.telegram.org/bot<TOKEN>/getUpdates`，从返回里找到 `chat.id`；
3. 用环境变量传入（**不要写在命令行参数里**，`ps` 能看到别人的进程参数）：

```sh
export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
export TELEGRAM_CHAT_ID=123456789
./chatgpt-plus-price-monitor -interval 30m -top 5 -threshold 10
```

## 选项

| 选项 | 默认值 | 说明 |
| --- | --- | --- |
| `-url` | priceai.cc 的商品页 | 监控的页面地址 |
| `-top` | `5` | 取前 N 个价格计算均价 |
| `-threshold` | `10` | 阈值（元），均价 ≤ 该值时通知 |
| `-sort` | `false` | 先按价格升序再取前 N（即"最便宜的 N 个"），默认按页面顺序 |
| `-selector` | 自动识别 | 价格元素的 CSS 选择器 |
| `-interval` | `0` | 轮询间隔（如 `30m`）；为 0 表示只检查一次就退出，交给 cron |
| `-timeout` | `30s` | 单次 HTTP 请求超时 |
| `-state` | `state.json` | 状态文件路径，用于通知去重 |
| `-cooldown` | `24h` | 价格持续低于阈值时的重复提醒间隔；`0` = 只在跌破那一刻提醒一次 |
| `-notify-recover` | `true` | 价格回升到阈值之上时也发一条 |
| `-telegram-token` | `$TELEGRAM_BOT_TOKEN` | Bot Token |
| `-telegram-chat` | `$TELEGRAM_CHAT_ID` | Chat ID |
| `-telegram-api` | `https://api.telegram.org` | API 地址，国内直连不通时可指向自建 Bot API 或反代 |
| `-dry-run` | `false` | 只抓取并打印，不发通知、不写状态 |
| `-dump` | | 把抓到的原始 HTML 存到文件，用于调试选择器 |
| `-verbose` | `false` | 打印完整价格列表和提取方式 |

## 几个设计上的说明

**为什么需要 Chat ID？**
Bot Token 只能证明"你是这个 bot"，还得告诉它把消息发给谁。两个都要。

**为什么有状态文件？**
如果每轮检查都推送，价格只要在阈值下待着，你半小时就会收到一条，很快就会把通知静音。
所以只在**状态发生变化**时通知：

- 均价从阈值之上跌到之下 → 发「降价提醒」
- 持续低于阈值 → 静默，直到超过 `-cooldown`（默认 24 小时）才再提醒一次
- 均价回升到阈值之上 → 发「价格回升」，之后恢复静默

删掉 `state.json` 就能重置状态。

**为什么抓到的价格数量不够会直接报错？**
如果页面改版导致只抓到 2 个价格，那这 2 个的均价并不是你想监控的东西，
却很可能凑巧低于阈值而误报。所以样本数少于 `-top` 时宁可报错退出（exit code 1），
也不发一条假的降价通知。

**`-sort` 要不要开？**
默认按页面顺序取前 N，对应你在网页上看到的前几行。如果页面本身不是按价格排序的，
或者你真正想要的是"最便宜的 N 个"，加上 `-sort`。

**关于 Telegram 在国内的连通性：**
`api.telegram.org` 在中国大陆无法直连。要么让程序走代理：

```sh
export HTTPS_PROXY=http://127.0.0.1:7890
```

要么用 `-telegram-api` 指向你自己的反代或
[自建 Bot API Server](https://github.com/tdlib/telegram-bot-api)。

## 抓不到价格怎么办

程序按 `-selector` → 页面内嵌 JSON（`__NEXT_DATA__` 等）→ 正文正则 的顺序尝试提取，
`-verbose` 会打印实际用的是哪一种。都失败时先把页面存下来：

```sh
./chatgpt-plus-price-monitor -dry-run -dump page.html
```

然后看 `page.html`：

- **里面有价格数字** → 用浏览器 F12 找到价格元素的 class，用 `-selector` 指定，例如
  `-selector '.price'`、`-selector 'td:nth-child(2)'`。这是最稳的方式。
- **里面只有个空壳 `<div id="app">`** → 页面是前端渲染的，纯 HTTP 请求拿不到数据。
  这时候翻一下浏览器 F12 的 Network 面板，多半能找到一个返回 JSON 的接口，
  直接把 `-url` 指向那个接口即可（程序也能从 JSON 响应里提价格）。
  实在不行才需要上无头浏览器。

## 定时运行

**crontab**（每 30 分钟，注意用绝对路径）：

```cron
*/30 * * * * TELEGRAM_BOT_TOKEN=xxx TELEGRAM_CHAT_ID=yyy /opt/monitor/chatgpt-plus-price-monitor -state /opt/monitor/state.json >> /var/log/price-monitor.log 2>&1
```

**systemd**（常驻，凭据放在 `/etc/price-monitor.env`，权限设成 600）：

```ini
[Unit]
Description=ChatGPT Plus price monitor
After=network-online.target

[Service]
EnvironmentFile=/etc/price-monitor.env
WorkingDirectory=/opt/monitor
ExecStart=/opt/monitor/chatgpt-plus-price-monitor -interval 30m -top 5 -threshold 10
Restart=always
RestartSec=60

[Install]
WantedBy=multi-user.target
```

## 测试

```sh
go test ./...
```

覆盖了三种提取策略（选择器 / 内嵌 JSON / 正文正则）、取前 N 与排序、
通知去重的状态机，以及 Telegram 客户端的成功与失败分支。
