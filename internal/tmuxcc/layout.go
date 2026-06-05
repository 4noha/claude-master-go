package tmuxcc

import (
	"errors"
	"strconv"
	"strings"
)

// Layout は tmux pane tree の解析結果。
//
// tmux layout string 形式 (man tmux "WINDOW AND PANE":
//
//	<cookie>,<width>x<height>,<x>,<y>,<pane-id>          # leaf pane
//	<cookie>,<width>x<height>,<x>,<y>{<child>,<child>,..} # 横分割 (左右)
//	<cookie>,<width>x<height>,<x>,<y>[<child>,<child>,..] # 縦分割 (上下)
//
// <cookie> は 4 桁 hex の check digit (本実装では parse して捨てる)。
// nested layout の child は同形式の再帰。
//
// 例:
//
//	"f30x100,0,0,0"                            → 単一 pane (id 0)
//	"f30x100,0,0{20x100,0,0,0,9x100,21,0,1}"   → 横 2 分割 (pane 0/1)
//	"f30x100,0,0[100x14,0,0,0,100x15,0,15,1]"  → 縦 2 分割 (pane 0/1)
type Layout struct {
	W, H, X, Y int     // pane / split の bbox
	PaneID     string  // leaf なら "%N" 形式 (tmux の pane id)、split なら ""
	Vertical   bool    // split kind: false=horizontal[{}] true=vertical[[]]
	Children   []*Layout // split 時のみ
}

// IsLeaf は leaf pane (= 子無し) か。
func (l *Layout) IsLeaf() bool { return len(l.Children) == 0 }

// ParseLayout は layout string を parse して Layout tree を返す。
// 例:
//
//	"f30x100,0,0,0"                     → leaf
//	"f30x100,0,0{20x30,0,0,0,9x30,21,0,1}" → horizontal split
//
// 先頭 cookie (4 桁 hex + ',') は skip。
func ParseLayout(s string) (*Layout, error) {
	// 先頭 cookie を skip (4-hex,)
	if len(s) < 5 || s[4] != ',' {
		return nil, errors.New("tmuxcc: missing cookie")
	}
	return parseNode(s[5:])
}

// parseNode は再帰的に layout text を Layout に変換する。
// 入力: "<W>x<H>,<X>,<Y>(,<paneid>|{children}|[children])..."
func parseNode(s string) (*Layout, error) {
	// W x H, X, Y, ...
	xi := strings.IndexByte(s, 'x')
	if xi < 0 {
		return nil, errors.New("tmuxcc: layout: missing 'x'")
	}
	w, err := strconv.Atoi(s[:xi])
	if err != nil {
		return nil, err
	}
	s = s[xi+1:]
	ci := strings.IndexByte(s, ',')
	if ci < 0 {
		return nil, errors.New("tmuxcc: layout: missing ',' after height")
	}
	h, err := strconv.Atoi(s[:ci])
	if err != nil {
		return nil, err
	}
	s = s[ci+1:]
	ci = strings.IndexByte(s, ',')
	if ci < 0 {
		return nil, errors.New("tmuxcc: layout: missing ',' after x")
	}
	x, err := strconv.Atoi(s[:ci])
	if err != nil {
		return nil, err
	}
	s = s[ci+1:]
	// Y の終わりは [,{[] のいずれか
	yEnd := strings.IndexAny(s, ",{[")
	if yEnd < 0 {
		// trailing y のみ (= 例外的・rare)
		yEnd = len(s)
	}
	y, err := strconv.Atoi(s[:yEnd])
	if err != nil {
		return nil, err
	}
	node := &Layout{W: w, H: h, X: x, Y: y}
	if yEnd >= len(s) {
		return node, nil
	}
	// 続く 1 byte で leaf or split を判定
	switch s[yEnd] {
	case ',':
		// leaf: pane id 続く (",N" 形式)
		// 末尾までが pane id (数字) のはず
		idStr := s[yEnd+1:]
		// 念のため後続文字を見る — split が連続する layout は無いはず
		// ただし child の場合 ',' 後に括弧無し pane が次々来るので
		// 呼出側 (parseChildren) で handle される
		// ここで来るのは tree root から再帰した leaf 系のみ。
		// numeric 部分のみ切り出し
		end := 0
		for end < len(idStr) && idStr[end] >= '0' && idStr[end] <= '9' {
			end++
		}
		node.PaneID = "%" + idStr[:end]
		return node, nil
	case '{':
		// horizontal split: children inside { ... }
		closing := matchBracket(s[yEnd:], '{', '}')
		if closing < 0 {
			return nil, errors.New("tmuxcc: layout: unbalanced {")
		}
		children, err := parseChildren(s[yEnd+1 : yEnd+closing])
		if err != nil {
			return nil, err
		}
		node.Children = children
		node.Vertical = false
		return node, nil
	case '[':
		// vertical split
		closing := matchBracket(s[yEnd:], '[', ']')
		if closing < 0 {
			return nil, errors.New("tmuxcc: layout: unbalanced [")
		}
		children, err := parseChildren(s[yEnd+1 : yEnd+closing])
		if err != nil {
			return nil, err
		}
		node.Children = children
		node.Vertical = true
		return node, nil
	}
	return node, nil
}

// matchBracket は s[0] が open bracket の前提で、対応 close の index
// (open からの offset) を返す。nested OK。失敗時 -1。
func matchBracket(s string, open, close byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseChildren は split の中身 ("WxH,X,Y(...)?,WxH,X,Y(...)?, ...")
// を top-level の child group に分割して parseNode する。
// top-level の ',' (= 同階層の child 区切り) を識別するため、{}/[] の
// nest depth を追跡しながら scan。
func parseChildren(s string) ([]*Layout, error) {
	var out []*Layout
	depth := 0
	start := 0
	// 各 child 単位は「WxH,X,Y[,id|{...}|[...]] パターン」だが、
	// trailing "N" (pane id) の終端も top-level ',' なので、4 番目の
	// ',' が来たら child 切り替えの可能性がある。leaf child は
	// "WxH,X,Y,N" (4 comma) で完結し、次の child は depth==0 で ','。
	// split child は "WxH,X,Y{...}" or "[...]"。
	// シンプル策: depth==0 の ',' でかつ「直前の token から見て次の
	// child 開始らしい」位置で split する。
	//
	// 確実な実装: child の境界は「数字始まりの "WxH,..." パターンを
	// 探す」。"NxN" pattern (数字+x+数字) があれば child 開始。
	// 但しコード簡素化のため、4 つの ',' depth=0 を 1 child とみなし、
	// 次に "x" を含む field 出現で次 child 開始とする。
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				// この ',' が child 区切りか pane-id 直前か判定:
				// 次の field が "<digit>x<digit>" なら child 開始
				if isChildBoundary(s[i+1:]) {
					child, err := parseNode(s[start:i])
					if err != nil {
						return nil, err
					}
					out = append(out, child)
					start = i + 1
				}
			}
		}
	}
	// 末尾の child
	if start < len(s) {
		child, err := parseNode(s[start:])
		if err != nil {
			return nil, err
		}
		out = append(out, child)
	}
	return out, nil
}

// isChildBoundary は s が "<digit>+x<digit>+,..." 形で始まれば true。
// pane id の直前 ',' (例 "WxH,X,Y,N" の N の前) は false。
func isChildBoundary(s string) bool {
	// 先頭から digit 連続を read、続けて 'x' があれば child の WxH 始まり
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0 && i < len(s) && s[i] == 'x'
}

// LeafPanes は Layout tree から leaf pane id 一覧を抽出する (順序保持)。
func (l *Layout) LeafPanes() []string {
	if l == nil {
		return nil
	}
	if l.IsLeaf() {
		if l.PaneID != "" {
			return []string{l.PaneID}
		}
		return nil
	}
	var out []string
	for _, c := range l.Children {
		out = append(out, c.LeafPanes()...)
	}
	return out
}
