package priceai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

var sampleResponse = []byte(`{"offers":[
	{"id":"a","sourceName":"源店铺","sourceStoreName":"Ai小卖部","sourceTitle":"GPT Plus 月卡","price":100.94,"currency":"CNY","url":"https://pay.ldxp.cn/item/wvlagr"},
	{"id":"b","price":101.97,"currency":"CNY"},
	{"id":"c","price":105.06,"currency":"CNY"},
	{"id":"d","price":116,"currency":"CNY"},
	{"id":"e","price":125,"currency":"CNY"}
],"total":5}`)

func TestParseRealResponse(t *testing.T) {
	offers, err := parse(sampleResponse, 5)
	if err != nil {
		t.Fatalf("parse 失败: %v", err)
	}
	if len(offers) != 5 {
		t.Fatalf("拿到 %d 条，期望 5 条", len(offers))
	}

	// 接口按价格升序返回，前 5 条就是最便宜的 5 条。
	want := []float64{100.94, 101.97, 105.06, 116, 125}
	for i, w := range want {
		if offers[i].Price != w {
			t.Errorf("第 %d 条价格 = %v, 期望 %v", i+1, offers[i].Price, w)
		}
	}

	first := offers[0]
	if first.Store() != "Ai小卖部" {
		t.Errorf("Store() = %q", first.Store())
	}
	if first.Currency != "CNY" {
		t.Errorf("Currency = %q", first.Currency)
	}
	if first.URL != "https://pay.ldxp.cn/item/wvlagr" {
		t.Errorf("URL = %q", first.URL)
	}
	if first.SourceTitle == "" {
		t.Error("SourceTitle 不应为空")
	}
}

// 真实返回里的价格既有小数也有整数（116、125），都要能解析。
func TestParseHandlesIntegerPrices(t *testing.T) {
	offers, err := parse(sampleResponse, 5)
	if err != nil {
		t.Fatalf("parse 失败: %v", err)
	}
	var sawInt bool
	for _, o := range offers {
		if o.Price == math.Trunc(o.Price) {
			sawInt = true
		}
		if o.Price <= 0 {
			t.Errorf("价格异常: %v", o.Price)
		}
	}
	if !sawInt {
		t.Error("测试数据里应当存在整数价格")
	}
}

// 返回条数不足时必须报错，否则会拿不完整的样本算出偏低的均价而误报。
func TestParseRejectsTooFewOffers(t *testing.T) {
	body := []byte(`{"offers":[{"id":"a","price":5,"currency":"CNY"},{"id":"b","price":6,"currency":"CNY"}],"total":2}`)
	if _, err := parse(body, 5); err == nil {
		t.Fatal("条数不足时期望报错")
	}
	// 正好够则应当通过。
	if _, err := parse(body, 2); err != nil {
		t.Errorf("条数正好够时不应报错: %v", err)
	}
}

// 阈值是按人民币比的，混进别的币种会把均价算错。
func TestParseRejectsForeignCurrency(t *testing.T) {
	body := []byte(`{"offers":[{"id":"a","price":5,"currency":"CNY"},{"id":"b","price":6,"currency":"USD"}],"total":2}`)
	if _, err := parse(body, 2); err == nil {
		t.Fatal("非 CNY 币种时期望报错")
	}
}

func TestParseRejectsBadPrice(t *testing.T) {
	body := []byte(`{"offers":[{"id":"a","price":0,"currency":"CNY"}],"total":1}`)
	if _, err := parse(body, 1); err == nil {
		t.Fatal("价格为 0 时期望报错")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := parse([]byte("<html>not json</html>"), 1); err == nil {
		t.Fatal("非 JSON 时期望报错")
	}
}

// Store() 在没有 sourceStoreName 时应回退到 sourceName。
func TestOfferStoreFallback(t *testing.T) {
	if got := (Offer{SourceStoreName: "店铺", SourceName: "源"}).Store(); got != "店铺" {
		t.Errorf("Store() = %q, 期望 店铺", got)
	}
	if got := (Offer{SourceName: "源"}).Store(); got != "源" {
		t.Errorf("Store() = %q, 期望 源", got)
	}
}

// 请求必须带 limit=N&offset=0，否则取到的不是最便宜的那几个。
func TestCheapestRequestParams(t *testing.T) {
	var gotQuery string
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sampleResponse)
	}))
	defer srv.Close()

	// 用测试服务器替换真实接口地址。
	old := apiEndpoint
	apiEndpoint = srv.URL
	defer func() { apiEndpoint = old }()

	offers, err := Cheapest(context.Background(), srv.Client(), 5)
	if err != nil {
		t.Fatalf("Cheapest 失败: %v", err)
	}
	if len(offers) != 5 {
		t.Errorf("拿到 %d 条，期望 5 条", len(offers))
	}
	if gotQuery != "limit=5&offset=0" {
		t.Errorf("query = %q, 期望 limit=5&offset=0", gotQuery)
	}
	for name, want := range map[string]string{
		"User-Agent":         userAgent,
		"Accept":             "*/*",
		"Accept-Language":    "zh-CN,zh;q=0.9",
		"Cache-Control":      "no-cache",
		"Pragma":             "no-cache",
		"Referer":            ProductPage,
		"Cookie":             "priceai_account_auth_hint=anonymous",
		"Priority":           "u=1, i",
		"Sec-CH-UA":          `"Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"`,
		"Sec-CH-UA-Mobile":   "?0",
		"Sec-CH-UA-Platform": `"Windows"`,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
	} {
		if got := gotHeaders.Get(name); got != want {
			t.Errorf("%s = %q, 期望 %q", name, got, want)
		}
	}
}

func TestCheapestNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	old := apiEndpoint
	apiEndpoint = srv.URL
	defer func() { apiEndpoint = old }()

	if _, err := Cheapest(context.Background(), srv.Client(), 5); err == nil {
		t.Fatal("HTTP 429 时期望报错")
	}
}
