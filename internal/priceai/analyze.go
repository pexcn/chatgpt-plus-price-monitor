package priceai

import "sort"

// Analysis 是一轮报价分析的结果。
type Analysis struct {
	// Median 是整个样本的中位数，作为"一份 Plus 大概值多少钱"的参考价位。
	Median float64
	// Floor 是可信价格的下限，等于 Median * floorRatio。
	Floor float64
	// Kept 是不低于 Floor 的报价，按价格升序。
	Kept []Offer
	// Dropped 是被判为异常规格而剔除的报价，按价格升序。
	Dropped []Offer
}

// Best 返回最便宜的可信报价。Kept 为空时第二个返回值为 false。
func (a Analysis) Best() (Offer, bool) {
	if len(a.Kept) == 0 {
		return Offer{}, false
	}
	return a.Kept[0], true
}

// Analyze 用样本的中位数当参照系，把明显低于价位的报价剔除掉。
//
// 卖家常把日抛、加购项、拼车之类的东西挂在同一个商品下，价格只有正常月卡的
// 零头。这些报价混进来会把均价拉低，触发一条根本买不到 Plus 的假通知。
//
// 用中位数而不是均值：均值本身就会被这些异常值拉偏，中位数不会。
// floorRatio 越小越宽松，1 表示只保留不低于中位数的报价。
func Analyze(offers []Offer, floorRatio float64) Analysis {
	sorted := make([]Offer, len(offers))
	copy(sorted, offers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Price < sorted[j].Price })

	a := Analysis{Median: median(sorted)}
	a.Floor = a.Median * floorRatio
	for _, o := range sorted {
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
