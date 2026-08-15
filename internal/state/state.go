// Package state 记录上一次的告警情况，用来做通知去重。
//
// 没有它的话，只要价格一直低于阈值，每个轮询周期都会推一条 Telegram，
// 很快就会被静音。
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	// Below 表示上一次检查时均价是否已低于阈值。
	Below bool `json:"below"`
	// LastNotify 是最近一次实际发出通知的时间。
	LastNotify time.Time `json:"last_notify,omitempty"`
	// LastAvg 是最近一次通知时的均价，用于价格回升时的对比文案。
	LastAvg float64 `json:"last_avg,omitempty"`

	// Failures 是连续抓取失败的次数，抓取成功时清零。
	//
	// 计数存在文件里而不是内存里，这样挂 cron 的单次模式（每轮都是新进程）
	// 也能累计。
	Failures int `json:"failures,omitempty"`
	// FailNotified 表示本轮故障已经发过告警，避免每次失败都推一条。
	FailNotified bool `json:"fail_notified,omitempty"`
}

// Load 读取状态文件。文件不存在时返回零值状态（等同于"从未告警过"）。
func Load(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// 状态文件损坏不应该让监控停摆，当作全新状态继续。
		return State{}, nil
	}
	return s, nil
}

// Save 原子地写回状态文件。
func Save(path string, s State) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
