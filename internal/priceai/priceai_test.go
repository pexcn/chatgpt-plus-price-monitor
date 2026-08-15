package priceai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// realResponse 是从 priceai.cc 抓下来的真实接口返回（limit=30&offset=60）。
func realResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/offers.json")
	if err != nil {
		t.Fatalf("读取测试数据失败: %v", err)
	}
	return b
}

func TestParseRealResponse(t *testing.T) {
	offers, err := parse(realResponse(t), 5)
	if err != nil {
		t.Fatalf("parse 失败: %v", err)
	}
	if len(offers) != 5 {
		t.Fatalf("拿到 %d 条，期望 5 条", len(offers))
	}

	// 接口按价格升序返回，前 5 条就是最便宜的 5 条。
	want := []float64{100.94, 101.97, 105.06, 107.64, 108.15}
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
	offers, err := parse(realResponse(t), 30)
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

func TestSummarizeRealResponse(t *testing.T) {
	offers, err := parse(realResponse(t), 5)
	if err != nil {
		t.Fatalf("parse 失败: %v", err)
	}
	st := Summarize(offers)
	// (100.94+101.97+105.06+107.64+108.15)/5
	if math.Abs(st.Avg-104.752) > 1e-9 {
		t.Errorf("Avg = %v, 期望 104.752", st.Avg)
	}
	if st.Min != 100.94 {
		t.Errorf("Min = %v, 期望 100.94", st.Min)
	}
	if st.Max != 108.15 {
		t.Errorf("Max = %v, 期望 108.15", st.Max)
	}
	if got := Summarize(nil); got != (Stats{}) {
		t.Errorf("空切片应返回零值, 得到 %+v", got)
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
	var gotQuery, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(realResponse(t))
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
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
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
