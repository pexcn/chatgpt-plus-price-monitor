package priceai

import "sort"

// Analysis 是一轮报价分析的结果。
type Analysis struct {
	// Median 是过滤后样本的中位数，作为"这档商品大概值多少钱"的参考价位。
	Median float64
	// Floor 是可信价格的下限，等于 Median * floorRatio。
	Floor float64
	// Kept 是通过全部过滤的报价，按价格升序。
	Kept []Offer
	// BelowMin 是低于 minPrice 被排除的报价，通常是另一档商品（日抛、试用）。
	BelowMin []Offer
	// Dropped 是低于 Floor 被剔除的报价，通常是同一商品下的其他规格。
	Dropped []Offer
}

// Best 返回最便宜的可信报价。Kept 为空时第二个返回值为 false。
func (a Analysis) Best() (Offer, bool) {
	if len(a.Kept) == 0 {
		return Offer{}, false
	}
	return a.Kept[0], true
}

// Analyze 分两步筛掉不该参与判断的报价。
//
// 第一步按 minPrice 排除掉整档不想要的商品。priceai.cc 上最便宜的几十条
// 是"未接码日抛号"（质保半小时），价格 18~27 元；月卡在 100 元以上。
// 两者标题和 filterTags 都可能被商家改，只有价格档位骗不了人。
// minPrice 为 0 时不做这一步。
//
// 第二步在剩下的报价里用中位数当参照系，剔除明显低于价位的那些 —— 卖家常把
// 加购项、拼车之类挂在同一个商品下，价格只有正常报价的零头。
// 用中位数而不是均值：均值本身就会被这些异常值拉偏。
//
// 顺序很重要：先排除掉另一档商品，中位数才落在你真正关心的价位上。
func Analyze(offers []Offer, minPrice, floorRatio float64) Analysis {
	sorted := make([]Offer, len(offers))
	copy(sorted, offers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Price < sorted[j].Price })

	var a Analysis
	candidates := sorted
	if minPrice > 0 {
		candidates = nil
		for _, o := range sorted {
			if o.Price < minPrice {
				a.BelowMin = append(a.BelowMin, o)
			} else {
				candidates = append(candidates, o)
			}
		}
	}

	a.Median = median(candidates)
	a.Floor = a.Median * floorRatio
	for _, o := range candidates {
		if o.Price < a.Floor {
			a.Dropped = append(a.Dropped, o)
		} else {
			a.Kept = append(a.Kept, o)
		}
	}
	return a
}

// median 返回已按价格升序排列的报价的中位数。
func median(sorted []Offer) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2].Price
	}
	return (sorted[n/2-1].Price + sorted[n/2].Price) / 2
}
