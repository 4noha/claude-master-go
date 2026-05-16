package screen

// ScrollRenderer は viewport のスクロール位置を保持し、論理 canvas
// (history.top + 可視 buffer) から viewport 行を切り出す。Python 版
// pty_scroll.ScrollRenderer の **先頭アンカー方式**を忠実移植。
//
// 不変条件（Python で苦労して確立）:
//   - スクロール位置は canvas 先頭(最古=0)からの絶対 anchor。最下部
//     基準だと遡り中に claude 出力で canvas が伸び表示がドリフトする。
//     先頭基準なら末尾追記で view が動かない（tmux copy-mode と同じ）。
//   - 最下部到達で follow（live 追従）へ復帰。
type ScrollRenderer struct {
	follow    bool
	anchor    int // not-follow 時の viewport 先頭絶対 oy
	lastMaxOy int // 直近 render の max_oy（Scroll() が screen 非依存なため）
}

func NewScrollRenderer() *ScrollRenderer { return &ScrollRenderer{follow: true} }

func (s *ScrollRenderer) FollowActive() bool { return s.follow }

func (s *ScrollRenderer) FollowBottom() { s.follow = true }

// Scroll: dy<0=上(古い方へ遡る), dy>0=下(新しい方へ)。Python 版 scroll
// と同一規則。clamp は ViewLines 時。
func (s *ScrollRenderer) Scroll(dy int) {
	if dy == 0 {
		return
	}
	if s.follow {
		if dy < 0 { // live から上へ遡り始める
			s.anchor = s.lastMaxOy + dy
			if s.anchor < 0 {
				s.anchor = 0
			}
			s.follow = false
		}
		// dy>0 で live 中は何もしない（最下部のまま）
		return
	}
	s.anchor += dy
	if s.anchor >= s.lastMaxOy {
		s.follow = true // 最下部到達 → live 追従
	} else if s.anchor < 0 {
		s.anchor = 0
	}
}

// Scrollback は互換: 「最下部から何行上か」。0=follow。
func (s *ScrollRenderer) Scrollback() int {
	if s.follow {
		return 0
	}
	d := s.lastMaxOy - s.anchor
	if d < 0 {
		return 0
	}
	return d
}

// ViewLines は論理 canvas = hist + vis から viewport(vrows 行)を返す。
// follow なら最下部、さもなくば先頭アンカー位置（末尾追記で不動）。
// 行数が足りない場合は空文字でパディングせず短い slice を返す
// （Python の render も canvas 末満で break）。
func (s *ScrollRenderer) ViewLines(hist, vis []string, vrows int) []string {
	total := len(hist) + len(vis)
	maxOy := total - vrows
	if maxOy < 0 {
		maxOy = 0
	}
	s.lastMaxOy = maxOy

	var oy int
	if s.follow {
		oy = maxOy
	} else {
		oy = s.anchor
		if oy >= maxOy {
			oy = maxOy
			s.follow = true // 最下部に達した → live
		} else if oy < 0 {
			oy = 0
		}
		s.anchor = oy
	}

	line := func(i int) string {
		if i < len(hist) {
			return hist[i]
		}
		j := i - len(hist)
		if j < len(vis) {
			return vis[j]
		}
		return ""
	}
	out := make([]string, 0, vrows)
	for r := 0; r < vrows; r++ {
		L := oy + r
		if L >= total {
			break
		}
		out = append(out, line(L))
	}
	return out
}

// Canvas は VT モデルの論理 canvas（hist + vis）行を返す補助。
func Canvas(v *VT) (hist, vis []string) {
	return v.HistoryLines(), v.VisibleLines()
}
