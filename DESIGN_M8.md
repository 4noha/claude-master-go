# DESIGN_M8 — Windows ネイティブ移植（設計確定ドラフト）

claude-master-go を **Windows ネイティブ**へ移植する。darwin/linux の
出荷品質を退行させない（CLAUDE.md「parity は cutover 解禁条件」）ことを
絶対条件に、Go の build tag による **OS-split（単一リポジトリ）** で行う。
別リポジトリ・フォークはしない（VT コアと回帰 fixtures は共有資産＝
二重化は乖離確定）。

## 0. 不変条件（M8 でも厳守）

- ヒューリスティック分類は一切しない。忠実 VT エミュ＋ viewport 再描画
  のみ（`internal/screen/*` は OS 非依存＝**移植で自動的に保持**）。
- 実テストで担保。Python `debug/fixtures/*/bytes.bin`（`resume-burst`
  等）はバイト列＝OS 非依存。**同じ回帰を Windows ビルドでも流す**＝
  最難関の正しさ保証がそのまま移植できる。合成で緑にしない。
- カーソル復元 / 先頭アンカー / quiescence ゲート / `\x1b[2J\x1b[9999;1H`
  クリアは screen 層内＝共有＝保持。
- darwin/linux の `go test ./...` 緑を CI で維持。OS-split は**加算のみ**。
  goreleaser は M8 完了まで linux/darwin。完了後 windows/amd64・arm64 追加。

## 1. OS 結合点（実コード確定・推測なし）

読み取りで確定した非テストコードの POSIX 結合（`file:line`）:

| シーム | 箇所 | POSIX 依存 | Windows 置換 |
|--------|------|-----------|--------------|
| **PTY** | `internal/ptyproxy/proxy.go:39,48,57,123-125` | `creack/pty`・`SysProcAttr{Setsid,Setctty}`・`syscall.EIO` | ConPTY（`CreatePseudoConsole`／検証済 lib） |
| **IPC listener** | `internal/ptyproxy/server.go:82` | `net.Listen("unix")` | named pipe（`Microsoft/go-winio`）or Win10 1803+ AF_UNIX |
| **IPC dial** | `internal/client/client.go:116`・`internal/monitor/resume.go:20`・`internal/cloud/relay/relay.go:187,204` | `net.Dial("unix")` | 同上（共通 transport 抽象） |
| **Resize 通知** | `internal/client/client.go:156`・`cmd/claude-master/main.go`（runProxy） | `signal.Notify(SIGWINCH)` | Windows コンソール resize（`ReadConsoleInput` WINDOW_BUFFER_SIZE_EVENT or `GetConsoleScreenBufferInfo` ポーリング） |
| **プロセス生死/kill** | `internal/monitor/monitor.go:275,319` | `syscall.Kill(pid,0)`・`SIGTERM` | `OpenProcess`/`GetExitCodeProcess`・`TerminateProcess`（or graceful: WM_CLOSE/CtrlEvent） |
| **デタッチ起動** | `internal/monitor/daemon.go:15,20` | `os.DevNull`・`SysProcAttr{Setsid}` | `NUL`・`SysProcAttr{CreationFlags: DETACHED_PROCESS\|CREATE_NEW_PROCESS_GROUP}` |
| **終了シグナル** | `cmd/claude-master/main.go`（sigDone/sigCtx） | `SIGTERM`/`SIGINT` | `os.Interrupt`（SIGTERM は Windows に無い） |
| **プロセス走査** | `internal/scanner/scanner.go`（`Scan`/`getCwdLsof`） | `ps aux`・`lsof` | Toolhelp32 列挙＋cmdline/cwd は WMI or `NtQueryInformationProcess` |
| **tmux 同期** | `internal/tmux/tmux.go` 全体 | `exec("tmux")` | ネイティブ等価物なし → **Windows ではスタブ（機能無効・graceful degrade）** |

OS 非依存（読み取りで確認・移植不要）: `internal/screen/*`（VT モデル＝
山場。`creack/pty`・`syscall` 非 import）、`internal/config`、
`internal/cloud/*` の大半（Firestore SDK・`coder/websocket`・`net/http`）。
要確認: `internal/selfupdate` の原子置換が Windows（`os.Rename` で
ハンドル保持中の exe 置換不可）で機能するか＝M8f で実検証。

純関数は共有・両 OS でテスト: `scanner` の `parsePSLine`/
`extractSessionID`/`splitWSN`/`unescapeLsof`、`tmux` の `shquote`/
`uniqueName`/`itoa`。OS-split は `Scan`/`getCwdLsof` 等の I/O 境界のみ。

## 2. シーム抽象（build tag）

`_unix.go`（`//go:build !windows`）/ `_windows.go`（`//go:build
windows`）でファイル分割。インターフェースは**最小**にし、`run.go`・
`server.go`・VT 投入・wire protocol（RESIZE/SCROLL マジック・frame）は
**無改変で共有**（＝cloud relay 経路も無改変＝既存 e2e 資産が活きる）。

1. **pty seam**（`internal/ptyproxy`）: `ptyEnd` interface
   `{ Setsize(c,r); Read([]byte); Write([]byte); Wait(); Close() }` を
   `Start()` が返す。`proxy_unix.go`=creack/pty、`proxy_windows.go`=
   ConPTY。`PumpToVT`/`Proxy` 構造体/`RunProxy` は共有。
2. **transport seam**（新規 `internal/ipc`）: `Listen(name)`/`Dial(name)`。
   unix=`net.Listen/Dial("unix")`、windows=named pipe。`server.go:82`・
   `client.go:116`・`resume.go:20`・`relay.go` は `ipc.*` 経由へ差し替え
   （ワイヤ・フレームは不変。fd 渡し非使用は確認済＝named pipe で安全）。
3. **resize seam**（新規 `internal/termsize`）: `Events() <-chan struct{}`。
   unix=SIGWINCH、windows=コンソールイベント。`client.go`・runProxy が利用。
4. **proc seam**（`internal/scanner` + `internal/monitor`）: `scan_*.go`
   と `procalive_*.go`/`prockill_*.go`。
5. **daemon/signal seam**（`internal/monitor` + `cmd`）: `daemon_*.go`・
   `signals_*.go`（終了シグナル集合の OS 差を吸収）。
6. **tmux seam**: `tmux_unix.go`=現実装、`tmux_windows.go`=`NewManager`
   が「tmux 非対応」エラー。monitor は tmux 欠如で graceful degrade
   （proxy/socket-client/cloud は動作。自動同期窓のみ無効）。

## 3. マイルストーン分割（鉄則: slice ごとに build＋実検証緑）

| M | 内容 | 実検証（合成不可） |
|---|------|-------------------|
| **M8a** | build tag スケルトン＋全シーム interface 化。`GOOS=windows go build` 緑＋ darwin/linux `go test ./...` 緑（parity guard） | 両 OS ビルド緑・既存全テスト緑 |
| **M8b ✅** | ConPTY pty backend | 実 `cmd.exe` を ConPTY backend で起動→`Start`→`PumpToVT`→`screen.VT` が出力描画を機械確認（`TestConPTYRealProgram_FeedsVTModel` PASS・3-OS build 緑） |
| **M8c ✅**（手動項目除く） | AF_UNIX 据置（named pipe 不要・実証）＋Windows resize source | IPC=AF_UNIX 実測 PASS＝shared 変更ゼロ。resize=`resize_windows.go` polling＋`TestPollResize` PASS。**統合: `TestConPTYProxyOverAFUnix_ClientReceivesRender` PASS**＝実 ConPTY proxy→server AF_UNIX listener→`net.Dial(unix)` client→再パース描画で M8C_OK 受信（Windows e2e・3-OS build 緑）。残（手動/follow-up）: 対話的コンソール resize→再描画は実端末必須／host 側 `notifyWinch` Windows polling |
| **M8d ✅**（cwd 等 best-effort） | Windows プロセス scanner | `scanner.go`=共有純パーサ／`scanner_unix.go`=ps+lsof（M8d 前とバイト同一＝parity）／`scanner_windows.go`=CIM(Win32_Process)。`TestScanDetectsRealClaudeNamedProcess` PASS＝実 CIM 列挙で実 claude 名プロセス検出。cwd は Windows で他プロセス PEB 読取要＝best-effort 空（ShortDir "unknown"）・StartTime/CPU/Mem 省略（同期に不要）。実 claude on Windows の argv 形（node.exe/claude.cmd 等）は claude 未導入で未確認＝follow-up |
| **M8e ✅**（monitor smoke は follow-up） | monitor on Windows（**psmux 実利用**・tmux スタブ廃止）＋デタッチ起動(M8a) | **再スコープ確定**: monitor が呼ぶ tmux メソッドは EnsureSession/AddWindow/ListWindows(#{window_name})/WindowFor/RenameWindow/RemoveWindow のみ＝**psmux 忠実機能だけで成立**。`@cm_remote`/`pane_current_command`(psmux 非忠実)を使う MarkedWindows/IsSocketClientRunning は **cloud agent 専用＝M8f へ移動**。tmux.go の POSIX シェル構築(`shquote`/`interactiveShell`)を OS-split（unix バイト同一＝parity）。`TestMonitorTmuxPathOnRealPsmux` PASS（実 psmux で上記全メソッド）。daemon/proc は M8a・scanner は M8d 済。残: monitor CmdStart/Stop/Status の Windows e2e smoke／`ShortDir` の `\` 非対応(cosmetic) |
| **M8f**（本PC検証可能分 ✅／(3)は要 Mac） | (1)selfupdate 原子置換 ✅ (2)psmux backend ✅ (3)cloud 実 GCP e2e=要 Mac | **(1) ✅**: `replaceSelf` 末尾 OS-split（`place_unix.go`=直接 rename・byte 同一＝parity／`place_windows.go`=実行中 exe を `.old` 退避→新配置→best-effort 削除）。`TestPlaceBinaryReplacesRunningExe` PASS。**(2) ✅**: cloud agent reconcile が依存する marker 機構を OS-split。`mark_unix.go`=現行 `@cm_remote` set-option/list-windows を **body バイト同一**＝parity／`mark_windows.go`=psmux 忠実プリミティブ（rename-window/`#{window_name}`/window_id）のみで marker を **window 名 base32 符号化**（exact-match 復号＝非ヒューリスティック・不変条件遵守）。`MarkedWindows`/`NewMarkedWindow`/`LegacyAttachWindows` は薄い委譲へ。`TestPsmuxMarkerRoundTripAndReconcile` PASS＝実 psmux で marker 厳密往復・dup 列挙・KillWindowID 反映。`IsSocketClientRunning` は cloud 非依存（M8e で確認済）。cosmetic: Windows のリモート窓表示名は符号化名（機能=reconcile/dedupe/restart 跨ぎは保持）。**(3) 未/要 Mac**: cloud パッケージは Windows build 緑(M8a)・AF_UNIX 動作(M8c)・tmux marker psmux 忠実(M8f2)。残るは実 GCP WSS e2e（wake→トンネル→display-oracle）＝SA 鍵/Cloud Run/Firestore 実環境要＝本 PC 不可＝Mac canonical で実施（M6e 同様）。selfupdate 実 DL は Release 発行後 |

各 M は「旧コードでそのテストが落ちる」を先に確認してから前進
（鉄則#2）。`resume-burst` 回帰は M8a 以降 Windows ビルドでも常時緑必須。

## 4. 設計上の決定と未決事項

- **決定（更新・M8c 実測 2026-05-17）**: 単一リポ＋build tag。IPC は
  **AF_UNIX 据置（named pipe 不採用）**。当初 named pipe(`go-winio`)を
  検討したが、Windows 11 で `net.Listen/Dial("unix")` の双方向＋
  `os.Remove` stale 除去＋close 後 re-listen が実使用パターン
  （SessionsDir 配下 .sock）で PASS と実測。よって `server.go`/
  `client.go`/`resume.go`/`relay.go` の既存 `unix` socket は **Windows で
  shared コード変更ゼロのまま動作**＝他環境に最もクリーン（go-winio
  依存も IPC 抽象も不要）。fd 渡し非使用も既確認。named pipe 化は将来
  AF_UNIX で問題が出た場合のみ（build-tag 隔離した `ipc` 抽象）。
- **決定（更新・スパイク実測済 2026-05-17）**: Windows の tmux は
  二段構え。**(a) 既定=graceful degrade**（M8a 実装済。tmux 不在で
  `NewManager` が err→自動同期窓のみ無効、proxy の per-client 多重化
  ＝端末共有本体は tmux 非依存で動く）。**(b) 強化=psmux backend**
  （github.com/psmux/psmux v3.3.4。ネイティブ Windows・ConPTY・Rust・
  MIT・活発）。
  **psmux 実コマンドスパイク結果**（実 `tmux.exe` を `internal/tmux`
  の exact コマンド列で叩いて確定。`cmd /c`/直接 `&` の正しい呼出で
  実測。当初の失敗は PS5.1 で native exe に `2>&1|Out-String` した
  ErrorRecord 化バグで psmux 起因ではない）:
  - ✅ 実 `tmux.exe`（psmux/pmux/tmux 同一バイナリ別名＝PATH 追加で
    `exec.LookPath("tmux")`/`exec.Command` 無改変動作）。
  - ✅ `has-session` 終了コード忠実（不在 rc=1／存在 rc=0＝`ok()`/
    `EnsureSession` 前提を満たす）。
  - ✅ **ヘッドレス常駐サーバが動作＋プロセス跨ぎ永続**（`start-server`
    →`new-session -d`→別プロセス `ls` で持続。monitor デーモン＝
    console 無し背景プロセスの利用形態が成立＝最難関クリア）。
  - ✅ `new-window -P -F "#{window_id}"`＝`@2` を印字。`window_name`
    も per-window 正確。
  - ✅ 非 tty `-F` 出力クリーン（`@1=MARK\n@2=MARK`、LF のみ・`=`
    保持・末尾 junk 無＝既存 `Split("\n")`/`IndexByte('=')`/`TrimSpace`
    がそのまま通る）。
  - ❌ **`@cm_remote` が per-window でない**（@2 のみ set→@1 にも
    漏洩＝全窓共有）。`MarkedWindows()` が全窓を remote 窓と誤認＝
    コードがコメントで「runaway 真因」と書く stateless 在席キー機構が
    破綻。
  - ❌ **`#{pane_current_command}` が固定 `shell`**（実プロセス名でない）
    ＝`IsSocketClientRunning` の self/python/bash 判定が常に不成立＝
    再アタッチ/重複防止が無効。
  **結論**: psmux は本物でサーバモデルも動くが**ドロップイン不可**。
  採用するなら **psmux 専用 Windows backend**（M8a の seam 位置に
  `tmux` の psmux 派生実装）で上記 2 つの非忠実機構を回避:
  - marker は per-window 不可の `@cm_remote` を捨て、psmux が忠実な
    **`#{window_name}` ＋既存 `keyToWindow`/STATUS_FILE 状態**で在席
    判定（`AdoptWindow`/復元機構は既にある）。
  - self 判定は `pane_current_command` を使わず、自前で「自分が作った
    窓/起動コマンド」を状態追跡（windows 側は別シグナル）。
  これは M8e（monitor on Windows）内の確定サブタスク（ゼロコードでは
  ない）。不採用時は (a) graceful degrade 据え置き（proxy/socket-client
  /cloud は Windows で動くため致命ではない）。
- **決定**: ConPTY 検証は Windows ネイティブで実施（鉄則#2。WSL/クロス
  コンパイルでは ConPTY 挙動を忠実検証できない）。開発は Windows
  ネイティブ Claude 主・WSL 従（[[windows-port-plan]]）。
- **決定（PoC 実証済 2026-05-17）**: ConPTY backend = **`github.com/
  UserExistsError/conpty` v0.1.4**。別モジュール PoC で実証: 生
  `x/sys/windows` 直叩き（CreatePseudoConsole＋STARTUPINFOEX＋
  PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE）は教科書通りでも子が
  pseudoconsole へ attach せず親コンソール継承＝本文がパイプに流れない
  ＝脆い。UserExistsError/conpty は子が正しく ConPTY 配下（hex で
  `ESC[?9001h…CONPTY_OK\r\n` をパイプ受信・親漏れ無し・`Wait` で
  exit code 取得）。API（`conpty.Start(cmdline, ConPtyDimensions(w,h))`
  →`io.ReadWriteCloser`＋`Resize(w,h)`＋`Wait(ctx)(code,err)`）が M8b
  backend IF（`master io.ReadWriteCloser`＋`Setsize`＋`Wait`＋`Close`）に
  ほぼ一致＝薄いアダプタで実装可。`proxy_windows.go`(`//go:build
  windows`)のみが import＝unix/darwin は非コンパイル＝creack/pty 据置
  ＝parity 無影響（go.mod に require 行が増えるのみ）。PoC（別 go.mod
  ＝本体非汚染）で決定を実証後、scaffold は撤去済（クリーン）。
- **M8b 実装知見（実挙動から確定・重要）**:
  1. **ConPTY はバイト透過でない**。unix PTY は子の書込をほぼ verbatim
     中継するが ConPTY は端末として再レンダリングし自前 VT を出力
     パイプへ出す。よって unix の `resume-burst` 録画(verbatim)を
     Windows/ConPTY で流して `proxy_test.go` の pyte しきい値と比較する
     のは林檎対蜜柑＝**直接適用不可**。Windows の display-oracle parity
     には ConPTY 実機で録ったフィクスチャが要る（将来サブタスク）。
     M8b ゲートは「実プログラム→ConPTY backend→Start→PumpToVT→
     screen.VT に既知文字列が描画される」ことを実証（適切な M8b 粒度）。
  2. **ConPTY は子終了で master を EOF しない**（pseudoconsole が出力
     write 側を保持＝`Close()`/ClosePseudoConsole まで Read が返らない）。
     コードベースは「子終了→master EOF→masterPump 終了→`srv.Done`」を
     前提（run.go・unix で真）。放置すると **本番 run.go も
     `<-srv.Done()` でハング**する実アーキ非互換。`proxy_windows.go` の
     winBackend が内部 goroutine で `cpty.Wait`（子終了）→`Close()` を
     駆動し、unix と同じ「子終了で master が閉じる」意味論を Windows でも
     成立させて解消（`closeOnce`＋teardown フラグで二重 Close／run.go の
     defer Close と整合）。
  3. unix 録画テスト群（`/bin/cat`,`/bin/sh`＋共有ヘルパ）は `!windows`
     タグ化＝Mac/linux は従来通り全実行（parity 無影響）、Windows は
     `proxy_conpty_windows_test.go` が実 ConPTY ゲート。`host_dispatch_test.go`
     は OS 非依存で両 OS で実行。
- **未決**: Windows でのデーモン常駐方式（Task Scheduler／Windows
  Service／スタートアップ）→ M8e で決定（macOS launchd 相当）。
- **前提条件（ブロッカー）**: 本 PC に Go 未導入（Windows/WSL 共）＝
  M8a 着手前に Windows ネイティブ Go 導入が必須。canonical repo は
  Mac 側（当ディレクトリは git 管理外）＝本書は正リポジトリへ反映する。
