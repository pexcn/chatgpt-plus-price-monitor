package state

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	tests := []struct {
		name          string
		prev          State
		below         bool
		cooldown      time.Duration
		notifyRebound bool
		want          Action
	}{
		{
			name:  "首次跌破阈值",
			prev:  State{Below: false},
			below: true, cooldown: day, notifyRebound: true,
			want: AlertBelow,
		},
		{
			name:  "持续低价且在冷却期内则静默",
			prev:  State{Below: true, LastNotify: now.Add(-2 * time.Hour)},
			below: true, cooldown: day, notifyRebound: true,
			want: Silent,
		},
		{
			name:  "持续低价且已过冷却期则重提醒",
			prev:  State{Below: true, LastNotify: now.Add(-25 * time.Hour)},
			below: true, cooldown: day, notifyRebound: true,
			want: RemindBelow,
		},
		{
			name:  "cooldown 为 0 时永不重复提醒",
			prev:  State{Below: true, LastNotify: now.Add(-365 * day)},
			below: true, cooldown: 0, notifyRebound: true,
			want: Silent,
		},
		{
			name:  "价格回升且开启回升通知",
			prev:  State{Below: true, LastNotify: now.Add(-time.Hour), LastAvg: 9.2},
			below: false, cooldown: day, notifyRebound: true,
			want: AlertRebound,
		},
		{
			name:  "价格回升但关闭回升通知",
			prev:  State{Below: true, LastNotify: now.Add(-time.Hour)},
			below: false, cooldown: day, notifyRebound: false,
			want: Silent,
		},
		{
			name:  "一直高于阈值",
			prev:  State{Below: false},
			below: false, cooldown: day, notifyRebound: true,
			want: Silent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.prev.Decide(tt.below, now, tt.cooldown, tt.notifyRebound); got != tt.want {
				t.Errorf("Decide() = %v, 期望 %v", got, tt.want)
			}
		})
	}
}

// 跌破阈值只应该触发一次告警，之后转入静默。
func TestDecideNoRepeatAlert(t *testing.T) {
	now := time.Now()
	prev := State{Below: false}
	if got := prev.Decide(true, now, 24*time.Hour, true); got != AlertBelow {
		t.Fatalf("第一次 = %v, 期望 AlertBelow", got)
	}
	prev = State{Below: true, LastNotify: now}
	if got := prev.Decide(true, now.Add(time.Minute), 24*time.Hour, true); got != Silent {
		t.Errorf("第二次 = %v, 期望 Silent", got)
	}
}
