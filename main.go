// Command chatgpt-plus-price-monitor 监控 priceai.cc 上的 ChatGPT Plus 报价。
package main

import (
	"os"

	"github.com/pexcn/chatgpt-plus-price-monitor/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:]))
}
