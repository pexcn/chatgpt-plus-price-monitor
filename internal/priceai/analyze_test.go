package priceai

import (
	"testing"
)

func priced(prices ...float64) []Offer {
	out := make([]Offer, 0, len(prices))
	for i, p := range prices {
		out = append(out, Offer{
			ID:              string(rune('a' + i)),
			SourceStoreName: "店铺",
			Price:           p,
			Currency:        "CNY",
		})
	}
	return out
}

// 核心场景：最低价是"别的规格"还是"真便宜"。
func TestAnalyzeDecision(t *testing.T) {
	const threshold, floorRatio = 10.0, 0.5

	tests := []struct {
		name       string
		prices     []float64
		wantBest   float64
		wantNotify bool
		wantDrop   int
	}{
		{
			// 2 只有中位数的零头，多半是某个规格的价格，不是 Plus 本身。
			name:   "极低价被剔除后不再触发",
			prices: []float64{2, 11, 11.5, 12, 11},
			// 剔除 2 之后最便宜的是 11，高于阈值。
			wantBest: 11, wantNotify: false, wantDrop: 1,
		},
		{
			// 9 和其他几个是一个量级，是真便宜。
			name:   "接近价位的低价保留并触发",
			prices: []float64{9, 11, 11.5, 12, 11},
			// 均价 10.9 高于阈值，但最低价 9 已经值得买了。
			wantBest: 9, wantNotify: true, wantDrop: 0,
		},
		{
			// 剔除异常值之后，第二便宜的仍然是个真实的好价。
			name:     "剔除异常值后仍能发现真实低价",
			prices:   []float64{2, 9, 11, 11.5, 12},
			wantBest: 9, wantNotify: true, wantDrop: 1,
		},
		{
			name:     "全部同价则不触发",
			prices:   []float64{11, 11, 11, 11, 11},
			wantBest: 11, wantNotify: false, wantDrop: 0,
		},
		{
			// 整体都便宜时是真实价位，不该当成异常。
			name:     "整体低价不算异常",
			prices:   []float64{2, 2.5, 3, 3, 3.5},
			wantBest: 2, wantNotify: true, wantDrop: 0,
		},
		{
			name:   "多个异常值一起剔除",
			prices: []float64{1, 2, 20, 21, 22},
			// 中位数 20，地板线 10，1 和 2 都被剔除。
			wantBest: 20, wantNotify: false, wantDrop: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Analyze(priced(tt.prices...), floorRatio)
			best, ok := a.Best()
			if !ok {
				t.Fatal("Best() 不应为空")
			}
			if best.Price != tt.wantBest {
				t.Errorf("Best = %v, 期望 %v", best.Price, tt.wantBest)
			}
			if got := best.Price <= threshold; got != tt.wantNotify {
				t.Errorf("是否通知 = %v, 期望 %v（Best=%v 阈值=%v）", got, tt.wantNotify, best.Price, threshold)
			}
			if len(a.Dropped) != tt.wantDrop {
				t.Errorf("剔除了 %d 条, 期望 %d 条", len(a.Dropped), tt.wantDrop)
			}
		})
	}
}

func TestAnalyzeSortsAndPartitions(t *testing.T) {
	// 乱序输入也要正确排序。
	a := Analyze(priced(12, 2, 11.5, 11, 11), 0.5)
	if a.Median != 11 {
		t.Errorf("Median = %v, 期望 11", a.Median)
	}
	if a.Floor != 5.5 {
		t.Errorf("Floor = %v, 期望 5.5", a.Floor)
	}
	wantKept := []float64{11, 11, 11.5, 12}
	if len(a.Kept) != len(wantKept) {
		t.Fatalf("Kept 有 %d 条, 期望 %d 条", len(a.Kept), len(wantKept))
	}
	for i, w := range wantKept {
		if a.Kept[i].Price != w {
			t.Errorf("Kept[%d] = %v, 期望 %v", i, a.Kept[i].Price, w)
		}
	}
	if len(a.Dropped) != 1 || a.Dropped[0].Price != 2 {
		t.Errorf("Dropped = %+v, 期望只有 2", a.Dropped)
	}
}

func TestMedian(t *testing.T) {
	if got := Analyze(priced(1, 2, 3), 0).Median; got != 2 {
		t.Errorf("奇数个: Median = %v, 期望 2", got)
	}
	if got := Analyze(priced(1, 2, 3, 4), 0).Median; got != 2.5 {
		t.Errorf("偶数个: Median = %v, 期望 2.5", got)
	}
	if got := Analyze(nil, 0.5).Median; got != 0 {
		t.Errorf("空输入: Median = %v, 期望 0", got)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	a := Analyze(nil, 0.5)
	if _, ok := a.Best(); ok {
		t.Error("空输入时 Best() 应返回 false")
	}
}

// floorRatio 为 1 时只保留不低于中位数的报价。
func TestAnalyzeFloorRatioOne(t *testing.T) {
	a := Analyze(priced(9, 11, 11.5, 12, 11), 1)
	if a.Floor != 11 {
		t.Errorf("Floor = %v, 期望 11", a.Floor)
	}
	best, _ := a.Best()
	if best.Price != 11 {
		t.Errorf("Best = %v, 期望 11", best.Price)
	}
}

func TestAnalyzeConcentratedPrices(t *testing.T) {
	a := Analyze(priced(100.94, 101.97, 105.06, 107.64, 108.15), 0.5)
	if len(a.Dropped) != 0 {
		t.Errorf("集中价位不该被剔除，实际剔除了 %d 条（中位数 %.2f，地板线 %.2f）",
			len(a.Dropped), a.Median, a.Floor)
	}
	best, ok := a.Best()
	if !ok || best.Price != 100.94 {
		t.Errorf("Best = %v, 期望 100.94", best.Price)
	}
}

func TestAnalyzeConcentratedDayPassPrices(t *testing.T) {
	a := Analyze(priced(18.69, 21.63, 22.15, 23.69, 27.04), 0.5)
	if len(a.Dropped) != 0 {
		t.Errorf("日抛价位集中，不该有报价被地板线剔除，实际剔除 %d 条", len(a.Dropped))
	}
	best, _ := a.Best()
	if best.Price != 18.69 {
		t.Errorf("Best = %v, 期望 18.69", best.Price)
	}
}
