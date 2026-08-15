package scraper

import (
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// 合理价格区间，用于过滤掉页面上的年份、销量、ID 等噪声数字。
const (
	minSanePrice = 0.01
	maxSanePrice = 1000000
)

// 带货币标记的价格：¥12.5 / ￥12.5 / 12.5元 / CNY 12.5
var currencyPriceRe = regexp.MustCompile(`[¥￥]\s*(\d+(?:\.\d{1,2})?)|(\d+(?:\.\d{1,2})?)\s*元|(?i:CNY|RMB)\s*(\d+(?:\.\d{1,2})?)`)

// 纯数字，用于 -selector 命中的元素文本本身就是价格的情况。
var bareNumberRe = regexp.MustCompile(`\d+(?:\.\d{1,2})?`)

// JSON 字段名中承载价格的关键词，锚定在结尾。
//
// 驼峰/下划线命名里结尾的名词决定了字段的类型，所以"以价格词结尾"这一条
// 就足够区分：unitPrice / sale_price / 售价 是金额，而 priceUnit（币种单位）、
// priceId、priceCount 不是。
var priceKeyRe = regexp.MustCompile(`(?i)(price|amount|cost|价格|售价|单价)$`)

// ParsePrice 从一段文本里取出第一个价格。优先认带货币标记的写法，
// 只有 allowBare 时才退化到"整段文本就是一个裸数字"的解析。
func ParsePrice(s string, allowBare bool) (float64, bool) {
	if m := currencyPriceRe.FindStringSubmatch(s); m != nil {
		for _, g := range m[1:] {
			if g == "" {
				continue
			}
			if f, err := strconv.ParseFloat(g, 64); err == nil && sane(f) {
				return f, true
			}
		}
	}
	if allowBare {
		if m := bareNumberRe.FindString(s); m != "" {
			if f, err := strconv.ParseFloat(m, 64); err == nil && sane(f) {
				return f, true
			}
		}
	}
	return 0, false
}

func sane(f float64) bool { return f >= minSanePrice && f <= maxSanePrice }

// pricesFromText 扫描整段可见文本，按出现顺序收集所有带货币标记的价格。
func pricesFromText(s string) []float64 {
	var out []float64
	for _, m := range currencyPriceRe.FindAllStringSubmatch(s, -1) {
		for _, g := range m[1:] {
			if g == "" {
				continue
			}
			if f, err := strconv.ParseFloat(g, 64); err == nil && sane(f) {
				out = append(out, f)
			}
			break
		}
	}
	return out
}

// pricesFromJSON 遍历一段 JSON（如 __NEXT_DATA__），按文档顺序收集价格字段。
//
// 这里用 json.Decoder 的 token 流而不是 unmarshal 到 map，因为 Go 的 map
// 是无序的，而"前 N 个"依赖于原始顺序。
func pricesFromJSON(data []byte) []float64 {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var out []float64
	if err := walkJSON(dec, "", &out); err != nil && err != io.EOF {
		return nil
	}
	return out
}

func walkJSON(dec *json.Decoder, key string, out *[]float64) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := kt.(string)
				if err := walkJSON(dec, k, out); err != nil {
					return err
				}
			}
			_, err = dec.Token() // 消费 '}'
			return err
		case '[':
			// 数组元素继承数组自身的字段名，这样 "prices": [9.9, 12] 也能命中。
			for dec.More() {
				if err := walkJSON(dec, key, out); err != nil {
					return err
				}
			}
			_, err = dec.Token() // 消费 ']'
			return err
		}
	case json.Number:
		if isPriceKey(key) {
			if f, err := v.Float64(); err == nil && sane(f) {
				*out = append(*out, f)
			}
		}
	case string:
		if isPriceKey(key) {
			if f, ok := ParsePrice(v, true); ok {
				*out = append(*out, f)
			}
		}
	}
	return nil
}

func isPriceKey(k string) bool {
	if k == "" {
		return false
	}
	return priceKeyRe.MatchString(k)
}
