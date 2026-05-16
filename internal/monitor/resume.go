package monitor

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Python resume_scheduler.py の移植。リセット時刻にセッションを再開
// するスケジューラ。pending は JSON 永続化（monitor 再起動跨ぎ）。

// SendToSocket は UNIX ソケットへ bytes 送信（接続不可なら false）。
// Python send_to_socket 同等（5s タイムアウト）。
func SendToSocket(socketPath string, data []byte) bool {
	c, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Write(data)
	return err == nil
}

var tzOffsetRe = regexp.MustCompile(`([+-])(\d{1,2})(?::(\d{2}))?`)

// parseTZOffset は "UTC+9"/"+9"/"UTC-5:30" 等から時間オフセットを返す
// （不明は 0）。IANA 名は Go の time.LoadLocation で解決。
func parseTZOffset(tz string) float64 {
	if tz == "" {
		return 0
	}
	if m := tzOffsetRe.FindStringSubmatch(tz); m != nil {
		sign := 1.0
		if m[1] == "-" {
			sign = -1
		}
		h, _ := strconv.Atoi(m[2])
		min := 0
		if m[3] != "" {
			min, _ = strconv.Atoi(m[3])
		}
		return sign * (float64(h) + float64(min)/60)
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		_, off := time.Now().In(loc).Zone()
		return float64(off) / 3600
	}
	return 0
}

var resetTimeLayouts = []string{
	"3:04 pm", "3:04pm", "3 pm", "3pm", "15:04",
}

// parseResetDatetime は "8:30 pm"+"UTC+9" 等を「次に来るその時刻」の
// aware time へ（Python _parse_reset_datetime と同一: 過ぎていれば翌日）。
func parseResetDatetime(timeStr, tzStr string) (time.Time, bool) {
	s := strings.ToLower(strings.TrimSpace(timeStr))
	if s == "" {
		return time.Time{}, false
	}
	var t time.Time
	ok := false
	for _, l := range resetTimeLayouts {
		if pt, err := time.Parse(l, s); err == nil {
			t = pt
			ok = true
			break
		}
	}
	if !ok {
		return time.Time{}, false
	}
	offH := parseTZOffset(tzStr)
	loc := time.FixedZone("reset", int(offH*3600))
	now := time.Now().In(loc)
	cand := time.Date(now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0, loc)
	if !cand.After(now) {
		cand = cand.Add(24 * time.Hour)
	}
	return cand, true
}

type pendingEntry struct {
	resetAt time.Time
	sock    string
}

type ResumeScheduler struct {
	file    string
	pending map[string]pendingEntry
}

func NewResumeScheduler(pendingFile string) *ResumeScheduler {
	s := &ResumeScheduler{file: pendingFile, pending: map[string]pendingEntry{}}
	s.load()
	return s
}

// Schedule は reset_at を計算して pending 登録（計算不可なら ok=false）。
func (s *ResumeScheduler) Schedule(ev *LimitEvent, sock string) (time.Time, bool) {
	at, ok := parseResetDatetime(ev.ResetTime, ev.ResetTZ)
	if !ok {
		return time.Time{}, false
	}
	s.pending[ev.SessionKey] = pendingEntry{resetAt: at, sock: sock}
	s.save()
	return at, true
}

// Due は now が reset_at を過ぎたものを返し pending から削除（Python due）。
func (s *ResumeScheduler) Due(now time.Time) [][2]string {
	var out [][2]string
	nu := now.UTC()
	for k, v := range s.pending {
		if !nu.Before(v.resetAt.UTC()) {
			out = append(out, [2]string{k, v.sock})
			delete(s.pending, k)
		}
	}
	if len(out) > 0 {
		s.save()
	}
	return out
}

func (s *ResumeScheduler) IsPending(key string) bool {
	_, ok := s.pending[key]
	return ok
}

func (s *ResumeScheduler) Remove(key string) {
	if _, ok := s.pending[key]; ok {
		delete(s.pending, key)
		s.save()
	}
}

func (s *ResumeScheduler) save() {
	m := map[string]map[string]string{}
	for k, v := range s.pending {
		m[k] = map[string]string{
			"reset_at":    v.resetAt.Format(time.RFC3339),
			"socket_path": v.sock,
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.file), 0o755) != nil {
		return
	}
	_ = os.WriteFile(s.file, b, 0o644)
}

func (s *ResumeScheduler) load() {
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var m map[string]map[string]string
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for k, v := range m {
		at, err := time.Parse(time.RFC3339, v["reset_at"])
		if err != nil {
			continue
		}
		s.pending[k] = pendingEntry{resetAt: at, sock: v["socket_path"]}
	}
}
