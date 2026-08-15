package state

import (
	"os"
	"path/filepath"
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
		notifyRecover bool
		want          Action
	}{
		{
			name:  "首次跌破阈值",
			prev:  State{Below: false},
			below: true, cooldown: day, notifyRecover: true,
			want: AlertBelow,
		},
		{
			name:  "持续低价且在冷却期内则静默",
			prev:  State{Below: true, LastNotify: now.Add(-2 * time.Hour)},
			below: true, cooldown: day, notifyRecover: true,
			want: Silent,
		},
		{
			name:  "持续低价且已过冷却期则重提醒",
			prev:  State{Below: true, LastNotify: now.Add(-25 * time.Hour)},
			below: true, cooldown: day, notifyRecover: true,
			want: RemindBelow,
		},
		{
			name:  "cooldown 为 0 时永不重复提醒",
			prev:  State{Below: true, LastNotify: now.Add(-365 * day)},
			below: true, cooldown: 0, notifyRecover: true,
			want: Silent,
		},
		{
			name:  "价格回升且开启回升通知",
			prev:  State{Below: true, LastNotify: now.Add(-time.Hour), LastAvg: 9.2},
			below: false, cooldown: day, notifyRecover: true,
			want: AlertRecover,
		},
		{
			name:  "价格回升但关闭回升通知",
			prev:  State{Below: true, LastNotify: now.Add(-time.Hour)},
			below: false, cooldown: day, notifyRecover: false,
			want: Silent,
		},
		{
			name:  "一直高于阈值",
			prev:  State{Below: false},
			below: false, cooldown: day, notifyRecover: true,
			want: Silent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.prev.Decide(tt.below, now, tt.cooldown, tt.notifyRecover); got != tt.want {
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

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")

	// 文件不存在时应返回零值而不是报错。
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load 不存在的文件报错: %v", err)
	}
	if s.Below {
		t.Error("零值状态的 Below 应为 false")
	}

	want := State{Below: true, LastNotify: time.Now().UTC().Truncate(time.Second), LastAvg: 9.62}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if got.Below != want.Below || !got.LastNotify.Equal(want.LastNotify) || got.LastAvg != want.LastAvg {
		t.Errorf("往返后 = %+v, 期望 %+v", got, want)
	}
}

func TestLoadCorruptedFileResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{ 这不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 状态文件损坏不应让监控停摆。
	s, err := Load(path)
	if err != nil {
		t.Fatalf("损坏的状态文件不应报错: %v", err)
	}
	if s.Below {
		t.Error("损坏后应回到零值状态")
	}
}
