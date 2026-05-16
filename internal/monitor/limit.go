package monitor

import "github.com/4noha/claude-master-go/internal/config"

// LimitWatcher は Python limit_watcher.py の移植。セッションごとに
// usage_percent を監視し、上位レベルへ上がった時だけ LimitEvent を返す。

// LimitLevel は approaching < interrupt < reached の順。
type LimitLevel int

const (
	LevelApproaching LimitLevel = 0
	LevelInterrupt   LimitLevel = 1
	LevelReached     LimitLevel = 2
)

func (l LimitLevel) String() string {
	switch l {
	case LevelApproaching:
		return "approaching"
	case LevelInterrupt:
		return "interrupt"
	default:
		return "reached"
	}
}

type LimitEvent struct {
	SessionKey   string
	Level        LimitLevel
	UsagePercent int
	ResetTime    string
	ResetTZ      string
}

type LimitWatcher struct {
	cfg      *config.Config
	notified map[string]LimitLevel
}

func NewLimitWatcher(cfg *config.Config) *LimitWatcher {
	return &LimitWatcher{cfg: cfg, notified: map[string]LimitLevel{}}
}

// statusInt は JSON 由来の数値（float64 等）を int へ。欠落は ok=false。
func statusInt(status map[string]any, key string) (int, bool) {
	v, ok := status[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func statusStr(status map[string]any, key string) string {
	if v, ok := status[key].(string); ok {
		return v
	}
	return ""
}

// Check は status の usage_percent を見て、レベルが前回より上がった
// 時だけ LimitEvent を返す（Python LimitWatcher.check と同一）。
func (w *LimitWatcher) Check(key string, status map[string]any) *LimitEvent {
	pct, ok := statusInt(status, "usage_percent")
	if !ok {
		return nil
	}
	var level LimitLevel
	switch {
	case pct >= 100:
		level = LevelReached
	case pct >= w.cfg.LimitInterruptPct:
		level = LevelInterrupt
	case pct >= w.cfg.LimitWarnPct:
		level = LevelApproaching
	default:
		delete(w.notified, key)
		return nil
	}
	if prev, seen := w.notified[key]; seen && level <= prev {
		return nil
	}
	w.notified[key] = level
	return &LimitEvent{
		SessionKey:   key,
		Level:        level,
		UsagePercent: pct,
		ResetTime:    statusStr(status, "reset_time"),
		ResetTZ:      statusStr(status, "reset_tz"),
	}
}

func (w *LimitWatcher) Clear(key string) { delete(w.notified, key) }
