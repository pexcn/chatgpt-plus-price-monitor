// Package state 保存通知去重所需的状态，并据此判断这一轮该不该通知。
//
// 状态只存在内存里，随进程生命周期存在：重启后会重新武装，
// 也就是说如果重启时价格仍低于阈值，会再收到一条降价提醒。
package state

import "time"

type State struct {
	// Below 表示上一次检查时均价是否已低于阈值。
	Below bool
	// LastNotify 是最近一次实际发出通知的时间。
	LastNotify time.Time
	// LastAvg 是最近一次通知时的均价，用于价格回升时的对比文案。
	LastAvg float64

	// Failures 是连续抓取失败的次数，抓取成功时清零。
	Failures int
	// FailNotified 表示本轮故障已经发过告警，避免每次失败都推一条。
	FailNotified bool
}

// Action 是根据当前价格与历史状态得出的通知决策。
type Action int

const (
	// Silent 表示无需打扰用户。
	Silent Action = iota
	// AlertBelow 表示价格刚跌破阈值。
	AlertBelow
	// RemindBelow 表示价格持续低于阈值，且已过冷却期。
	RemindBelow
	// AlertRebound 表示价格回升到阈值之上。
	AlertRebound
)

func (a Action) String() string {
	switch a {
	case AlertBelow:
		return "alert"
	case RemindBelow:
		return "remind"
	case AlertRebound:
		return "rebound"
	default:
		return "silent"
	}
}

// Decide 判断这一轮该不该通知。
//
// below         本轮均价是否低于阈值
// cooldown      持续低于阈值时的重复提醒间隔；<=0 表示只在跌破的那一刻通知一次
// notifyRebound 价格回升时是否也发一条
func (s State) Decide(below bool, now time.Time, cooldown time.Duration, notifyRebound bool) Action {
	if below {
		if !s.Below {
			return AlertBelow
		}
		if cooldown > 0 && !s.LastNotify.IsZero() && now.Sub(s.LastNotify) >= cooldown {
			return RemindBelow
		}
		return Silent
	}
	if s.Below && notifyRebound {
		return AlertRebound
	}
	return Silent
}
