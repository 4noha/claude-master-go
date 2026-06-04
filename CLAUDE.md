# claude-master-go プロジェクト

Claude Code セッション監視・tmux 自動同期 + ミニ tmux PTY プロキシ。
Python 版（`~/works/claude‐master`）の **Go 移植**。完全静的単一バイナリ
配布・GCP クラウド同期を目指す。設計詳細は [DESIGN.md](DESIGN.md)。

## ステータス / 移行方針（重要）

- **Python 版は放棄（新規投資しない）。知見は本ファイルに集約。**
- 稼働ベース（`claude-wrap`/launchd/このPC）の実切替は **Go が M2〜M5 を
  実録画検証で通ってから**。それまで Python を動かし続ける（環境を壊さない）。
  `~/.claude-master.toml` は両者同一キー＝設定移行不要（M1 で parity 確認済）。
- マイルストーン: **M1 config ✅** / **M2 VT モデル ✅** / **M3 ptyproxy ✅** /
  **M4 nav・pagekey・wheel・SESSION_LOG ✅** /
  **M5 monitor・tmux・socket_client・実行可能 proxy ✅（完全）** /
  **M6 GCP 同期（設計確定: `DESIGN_M6.md`。実装中）** /
  **M8 Windows ネイティブ移植（`DESIGN_M8.md`。M8a ✅ seam OS-split／
  M8b ✅ ConPTY backend〔UserExistsError/conpty・実テスト緑〕／M8c
  ✅〔IPC=AF_UNIX 据置実証＝shared 変更ゼロ・resize source 実装・実
  ConPTY proxy⇔AF_UNIX client 統合テスト緑〕／M8d ✅〔scanner OS-split・
  実 CIM 検出テスト緑・cwd 等 best-effort〕／M8e ✅〔monitor の tmux 経路を
  実 psmux で検証緑・tmux.go POSIX シェル構築 OS-split・psmux 非忠実
  workaround は M8f へ再スコープ〕／M8f〔(1)selfupdate Windows 原子置換
  ✅ (2)psmux backend ✅ (3)cloud 実 GCP e2e=要 Mac〕／**M8g ✅〔実
  claude 導入・実環境 runtime 検証＝scanner 引用符 argv／REAL_CLAUDE
  .exe／cwd→short_dir／窓名追随／proxy カットオーバー(claude シム)／
  S4U 常駐／M8f2 リモート窓 POSIX→PowerShell storm 真因修正＋marker
  表示整形 を実 CIM/実 psmux/実 reconcile トレースで修正・検証〕**。
  **本 PC 検証可能な Windows 移植は M8a–M8g 全て実テスト緑・unix
  バイト同一＝parity。残=Mac canonical 全 go test parity/cloud 実
  GCP e2e**）**。
  各 M は build＋
  実録画/実環境テスト緑で前進（合成では緑判定しない）。
  - **M8 Windows 移植（設計確定: `DESIGN_M8.md`。M8a ✅）**: 単一リポ
    ＋build tag の OS-split（フォークしない＝VT コア/回帰 fixtures は
    共有資産）。設計の OS 結合点は DESIGN_M8.md（pty→ConPTY、IPC unix
    socket→named pipe〔fd 渡し非使用確認済〕、SIGWINCH→コンソール
    resize、ps/lsof→Toolhelp32、`syscall.Kill`→`OpenProcess`/
    `TerminateProcess`、setsid→DETACHED_PROCESS、tmux は Windows
    非対応＝graceful degrade）。`internal/screen/*`〔VT 山場〕は OS
    非依存で移植不要。
    - **M8a ✅（Go go1.26.3 導入: Windows `~/go-sdk`・User PATH 永続）**:
      実コンパイル阻害は実測 **3 ファイル5箇所のみ**だった（`pty.*`/
      `net "unix"`/`ps`/`lsof`/`tmux` exec は Windows でもコンパイル可
      ＝それらの **runtime** 対応は M8b–d）。OS-split seam 実装:
      `ptyproxy.childSysProcAttr`・`client.watchResize`・
      `monitor.detachSysProcAttr/procAlive/procTerminate`・
      `main.notifyWinch`（各 `_unix.go`/`_windows.go`）＋ cloud e2e
      `_test.go` 4 本を `!windows` タグ化（POSIX エミュレータ専用＝
      Mac/linux では従来通り実行＝parity 無影響）。**検証（機械確認）**:
      `GOOS=windows/linux/darwin go build ./...` 全緑、windows vet ==
      linux vet（M8a 由来差分ゼロ）、OS 非依存共有コア
      `internal/screen`〔VT〕・`internal/config` は windows host
      `go test` 緑。**未検証（要 Mac canonical）**: darwin/linux 全
      `go test ./...`（実 pty/socket/tmux・`resume-burst`
      display-oracle）は本 PC で実行不可＝Mac 正環境で要 parity 実行。
      **既存・スコープ外**: `cmd/claude-master/main.go:353/369` の
      lostcancel vet 警告は M8a 前から linux でも出る既存
      （`runCloudEnroll`・未変更）＝M8a では触れない（鉄則#1）。
    - **M8b ✅ ConPTY backend**: PoC で生 x/sys 直叩きは子が
      pseudoconsole 非 attach＝脆いと実証→`UserExistsError/conpty`
      採用。`proxy.go` を backend IF 化（`master`=`io.ReadWriteCloser`）
      ＋`proxy_unix.go`〔creack/pty・旧 proxy.go とバイト同一＝parity〕
      ＋`proxy_windows.go`〔conpty〕。**実挙動知見**: ConPTY は
      バイト透過でなく再レンダリング＝unix resume-burst pyte しきい値は
      Windows 直接適用不可（Windows 用 ConPTY フィクスチャは将来）。
      ConPTY は子終了で master を EOF しない（pseudoconsole 保持）＝
      run.go の `<-srv.Done()` ハング非互換を winBackend 内
      `cpty.Wait→Close` で unix 同等意味論に橋渡し。実 `cmd.exe`→
      ConPTY→Start→PumpToVT→screen.VT を `TestConPTYRealProgram_
      FeedsVTModel` で機械確認（3-OS build 緑）。unix 録画テストは
      `!windows` タグ化＝Mac/linux 従来通り（parity 無影響）。
    - **M8c ✅（手動項目除く）**: IPC は Windows で
      `net.Listen/Dial("unix")` 双方向＋stale 除去＋再 listen が実使用
      パターンで PASS と実測＝**named pipe 不要・shared コード変更
      ゼロ**（他環境最クリーン）。Windows コンソール resize source を
      `resize_windows.go` に polling 実装（`watchResize()` 署名不変＝
      client.go/resize_unix.go 変更ゼロ）・`TestPollResize` PASS。
      **統合 `TestConPTYProxyOverAFUnix_ClientReceivesRender` PASS**＝
      実 ConPTY proxy→`server.go` AF_UNIX listener→`net.Dial(unix)`
      client→再パース描画で M8C_OK 受信（Windows e2e）。
      `client_test.go`（/bin/sh・/tmp 依存）は `!windows` タグ化
      （Mac/linux 従来通り＝parity）。**残（手動/follow-up）**: 対話的
      コンソール resize→再描画は実端末必須（harness 不可・鉄則#2
      honest）／host 側 `notifyWinch` Windows polling。
    - **M8d ✅**: `scanner.go`=共有純パーサ（型/parsePSLine/
      extractSessionID/splitWSN/unescapeLsof）／`scanner_unix.go`=
      ps+lsof（M8d 前とバイト同一＝parity）／`scanner_windows.go`=
      CIM(Win32_Process) で PID＋CommandLine 列挙→共有 extractSessionID
      /winClaudeBase（exact-match・非ヒューリスティック）。実 CIM で実
      claude 名プロセス検出を `TestScanDetectsRealClaudeNamedProcess`
      PASS。cwd は Windows で他プロセス PEB 読取要＝best-effort 空・
      StartTime/CPU/Mem 省略（同期に不要）。`scanner_test.go`(ps/lsof/
      sleep)は `!windows` タグ化＝Mac/linux 従来通り（parity）。実
      claude on Windows の argv 形は claude 未導入で未確認＝follow-up。
    - **M8e ✅**: 実コード確定で**再スコープ**＝monitor が呼ぶ tmux は
      EnsureSession/AddWindow/ListWindows(#{window_name})/WindowFor/
      RenameWindow/RemoveWindow のみ＝psmux 忠実機能だけで成立。psmux
      非忠実(@cm_remote/pane_current_command)依存の MarkedWindows/
      IsSocketClientRunning は cloud agent 専用＝**M8f へ移動**。tmux.go
      の POSIX シェル構築（`shquote`/`interactiveShell`）を OS-split
      （`quote_unix.go` は body バイト同一＝parity／`quote_windows.go`）。
      `TestMonitorTmuxPathOnRealPsmux` PASS（実 psmux で当該全メソッド）。
      monitor/tmux の unix テストは `!windows` タグ化（Mac/linux 不変）。
      残(follow-up): monitor CmdStart/Stop/Status の Windows e2e smoke／
      `ShortDir` の `\` 非対応(cosmetic)。
    - **M8f**: **(1) selfupdate 原子置換 ✅**＝`replaceSelf` 末尾を
      OS-split（`place_unix.go`=直接 rename・byte 同一＝parity／
      `place_windows.go`=実行中 exe を `.old` 退避→新配置→best-effort
      削除）。`TestPlaceBinaryReplacesRunningExe` PASS（実行中実 exe）。
      **(2) psmux backend ✅**＝cloud agent reconcile 依存の marker 機構
      を OS-split。`mark_unix.go`=現行 `@cm_remote` set-option/
      list-windows を **body バイト同一**＝parity／`mark_windows.go`=
      psmux 忠実プリミティブ（rename-window/`#{window_name}`/window_id）
      のみで marker を **window 名 base32 符号化**（exact-match 復号＝
      非ヒューリスティック）。`MarkedWindows`/`NewMarkedWindow`/
      `LegacyAttachWindows` は薄い委譲化。`TestPsmuxMarkerRoundTrip
      AndReconcile` PASS（実 psmux で marker 厳密往復・dup 列挙・kill
      反映）。`IsSocketClientRunning` は cloud 非依存(M8e 確認済)。
      cosmetic（→ M8g で解消）: リモート窓表示名は符号化名だったが、
      M8g で `window-status-format` 置換により marker を**表示から
      除去**（窓名実体は marker 保持＝reconcile 無影響）。
      **(3) cloud 実 GCP e2e=要 Mac**＝build 緑(M8a)・AF_UNIX(M8c)・
      tmux marker psmux 忠実(M8f2)。残るは実 GCP WSS e2e（SA 鍵/
      Cloud Run/Firestore 実環境要）＝本 PC 不可＝Mac canonical で実施
      （M6e 同様）。
    - **M8g ✅（実 claude 導入・実環境 runtime 検証＝鉄則#1/#2 で
      実再現後に修正）**: claude を実導入したら M8a–f の build 検証
      では出ない runtime 穴が連鎖発覚。全て実環境で実再現→修正→
      実検証。
      - **scanner 引用符 argv**: 実 claude は `"C:\…\claude.exe"
        --resume <uuid>` と**明示引用符付き**。`strings.Fields` だと
        `fields[0]` に `"` が残り `winClaudeBase` 誤判定＝検出不能。
        `scanner_windows.go` を `splitWinCmdline`（CommandLineToArgvW
        忠実）へ。実 argv 回帰テスト追加（旧落ち/新緑・実 CIM 緑）。
      - **REAL_CLAUDE 既定**: `~/.local/bin/claude`（`.exe` 無し）で
        `os.Stat` 不可＝proxy 即失敗。`config.go` を OS 対応既定
        （windows=`.exe`／unix バイト同一＝parity）。
      - **セッション名 unknown**: Windows scanner は他プロセス cwd
        非解決（M8d best-effort）。`statuswriter.go` を **Windows
        限定**で cwd/short_dir を `<pid>.status.json` へ出力→
        `WriteStatus` merge で Web/STATUS の short_dir が実フォルダ名
        （unix は scanner(lsof) 解決済＝非出力＝STATUS バイト不変）。
        併せて `monitor.go` RunLoop が「proxy 報告 short_dir 判明時、
        窓名が stale な unknown なら一度 RenameWindow」追随（unix は
        status に short_dir 非在＝no-op＝parity）。M8e の `ShortDir
        \`／窓名 cosmetic を解消。
      - **proxy カットオーバー（Windows 版 claude-wrap）**: monitor は
        `managedOnly`＝`<pid>.sock` を持つ proxy 経由 claude のみ
        STATUS へ。素起動 claude は対象外（仕様）。Windows は
        `claude` シム未設定だった＝Web 不出の主因。運用設定（リポ外・
        既存 `cloud-agent.cmd` と同列）: `~/.claude-master/bin\
        claude.cmd`→`claude-master proxy %*`＋User PATH 前置、
        `monitor.cmd`、`*.cmd` ASCII 化（日本語 rem を cmd.exe が
        OEM 誤実行する実バグ修正）。
      - **S4U 常駐（Mac launchd 相当）**: monitor/cloud を
        InteractiveToken スケジュールタスクで起動すると proxied claude
        の console 制御イベント等で `0xC000013A` 終了し RestartOnFailure
        も復帰させない脆弱性を確認。`LogonType=S4U`（Session 0・非
        対話・ログオフ耐性・RestartOnFailure 機能）へ。cloud の外向き
        WSS/Firestore は S4U で疎通実証。運用（リポ外）: タスク
        `claude-master-monitor`/`-cloud`＋`apply-s4u.ps1`/
        `apply-winfix.ps1`。
      - **M8f2 リモート窓 runaway storm（真因）**: tmux 越しに他 PC
        窓名が base32 で出没を繰り返す重度 churn を実報告。実 psmux＋
        実 reconcile トレース（CM_DEBUG）で確定: cloud agent の `wc`
        が **POSIX sh 構文**だが psmux `default-shell=powershell.exe`
        ＝即エラー→pane 終了→`remain-on-exit off` で**窓が生成直後
        消滅**→reconcile `cur=0`→毎周 6 窓再作成の自走ストーム。
        修正: `cmd/claude-master/remotecmd_{unix,windows}.go`(新) で
        `wc` を OS-split（unix=現 POSIX **バイト同一**＝parity／
        windows=PowerShell・実 psmux で生存継続実証）。併せて
        `tmux.go`＋`mark_{unix,windows}.go` に `initialName`（windows
        は marker を**生成時から窓名へ原子的に**符号化＝marker-less な
        隙間消滅／unix はラベルそのまま＝`new-window -n` 引数バイト
        同一）と Windows `legacyMarkerlessIDs`→nil（in-flight 窓誤殺
        ＝増幅器除去）。検証: `desired=6 cur=6` 毎周 KEEP・CREATE 0・
        psmux window_id 固定（churn 全停止）。
      - **marker 表示整形**: `EnsureSession` に OS-split
        `applyWindowDisplayFormat`（windows のみ
        `window-status-format`=`#I:#{s/ cmr1_.*//:window_name}#F`＝
        status-bar から marker トークン除去・**raw `#{window_name}` は
        marker 保持**＝`listWindowMarkers`/reconcile 無影響／unix=空
        body＝EnsureSession 発行コマンド M8 前と同一＝parity）。実
        psmux で置換適用・raw 保持・storm 非回帰を実検証。
      - 全変更 3-OS ビルド緑・unix バイト同一（`git diff` で
        mark_unix.go は追加関数のみ／remotecmd_unix.go は同一 Sprintf
        を確認）。既存 `cmd/.../main.go:353/369` lostcancel は M8 前
        から linux でも出るスコープ外（未変更）。
    - **本 PC 検証可能な Windows 移植は M8a–M8g 全て実テスト緑・
      unix バイト同一（他環境 parity）**。残作業（全て要 Mac
      canonical）: ①darwin/linux 全 `go test ./...`（実 pty/socket/
      tmux・resume-burst display-oracle）parity 実走 ②cloud 実 GCP
      WSS e2e ③既存 `cmd/.../main.go:353` lostcancel(M8 前から linux
      でも出る・スコープ外)。M8a–g 差分は本コミットで正リポジトリへ
      反映済。開発は Windows ネイティブ Claude 主・WSL 従。
  - M6 設計確定: wake=Firestore listener（FCM 不採用＝デスクトップ常駐
    向き・near-$0・PC 発 NAT 越え）、データ線=Cloud Run WSS で既存
    RESIZE/SCROLL/frame protocol をトンネル（`internal/client` 再利用・
    新プロトコル無し）、quiescence で開閉。
  - **M6a–d ✅**（実 WSS／gcloud Firestore エミュレータ＝実 API／実
    PtyProxy＋実録画／display-oracle で全検証。Cloud Functions は
    viewer 直接 wake 書込で不要化＝簡素化）。
  - **M6e ✅**（ユーザー承認の上 実 GCP デプロイ済）: 専用 project
    `example-gcp-project`／Cloud Run relay `claude-master-relay`
    （`https://claude-master-relay-demo01-an.a.run.app`、min0・
    timeout3600・no-cpu-throttling）／Firestore Native(asia-northeast1)
    ／SA `cm-agent`(datastore.user, 鍵 `~/.claude-master/sa.json`)。
    実 Cloud Run＋実 Firestore 経由 e2e（wake→トンネル→display-oracle）
    緑（`go test -tags manual` の `TestE2ERealGCP`）。**`/healthz` は
    GFE 予約で遮断＝ヘルスは `/`**。Python 版に無いクラウド同期を Go で
    新規達成。
  - **cloud agent 常駐化 ✅**: `~/Library/LaunchAgents/
    com.4noha.claude-master-cloud.plist`（RunAtLoad/KeepAlive、env に
    GOOGLE_APPLICATION_CREDENTIALS/GCP_PROJECT/CLOUD_RELAY_URL、ログ
    `~/.claude-master-cloud.log`）。KeepAlive 自動復帰検証済。near-$0
    維持のため `state.PushStatus` は content_hash 不変時 Firestore 非
    書込（毎 tick 全書込＋無駄 wake を回避）。**プロセス終了同期**:
    producer ループが前 tick との in-memory 差分で消滅キーを
    `state.DeleteSession` で削除（起動時 `OwnSessionKeys` で prev を
    seed＝再起動跨ぎ取りこぼし防止）。終了が WatchSessions→
    ReconcileRemote へ push 伝播し ↗窓/dashboard 行が消える。追加読み
    無し・終了時の Delete 書込のみで near-$0 維持。停止は
    `launchctl unload -w …com.4noha.claude-master-cloud.plist`。
  - 稼働 launchd 2 本: `com.4noha.claude-master`(monitor)/
    `com.4noha.claude-master-cloud`(cloud agent)。SA 鍵
    `~/.claude-master/sa.json` はリポジトリ外・600・非コミット。
  - **M7 Web 管理 UI＋Google ログイン ✅（実デプロイ・実ブラウザ検証済）**:
    Cloud Run relay を Web 兼用へ拡張（ブラウザは GCP 資格情報を持たず
    HMAC cookie、Firestore は Cloud Run ランタイム SA 経由。relay の
    バイト透過/既存 /session は無改変）。M7a-d=Web/xterm/実 e2e、
    M7e=3 ページ化（`/`＝アカウント端末一覧→各セッションから
    `/term` の Web ターミナルへリンク）、**M7f=認証を Google アカウント
    に置換**（pairing code 廃止。GIS→`/auth/google`：g_csrf 二重送信→
    idtoken 検証→`ALLOWED_EMAILS`=owner@example.com のみ→cookie
    scope="*"）。OAuth 同意画面は External/Testing＋テストユーザー、
    Web Client ID は env、起動時 `RegisterPC` で端末一覧に確実表示。
    **実 Chrome で実 Google サインイン→端末一覧→Web ターミナル→
    /term xterm DOM に実録画フッター**を display-oracle 確認（合成なし）。
    公開 `https://claude-master-relay-demo01-an.a.run.app/`。
    署名鍵 `~/.claude-master/web_signing_key`/SA 鍵 `~/.claude-master/
    sa.json`（共にリポジトリ外）。個人専用前提＝審査・公開不要。
    別 PC は「ブラウザで公開 URL を開き Google ログイン」だけで接続可。
    M7g=Web「＋ 端末を追加」: `POST /api/enroll`（所有者のみ）が一回
    限り enroll コード発行→新 PC で `claude-master cloud enroll <code>
    --relay wss://…`→`/enroll` が一回消費し sa.json＋設定を自動配置→
    `cloud agent` 起動で端末一覧に追加。SA 鍵は env ENROLL_SA_JSON /
    _B64。実 Chrome で「端末を追加」→実 relay 交換→sa.json/toml 配置を
    実環境検証（rev 00006）。実 Mac-Studio が 4 セッションで一覧表示済。
  - **M8 Web 運用機能 ✅（owner 限定・実 Firestore 検証）**: 運用負荷
    軽減の遠隔運用 3 本。**Phase1 版可視化**: proxy が自版を
    `<pid>.status.json` `cm_version` へ→monitor merge→Firestore（プロセス
    毎定数＝content_hash 初回1回＝near-$0、旧 inode proxy は旧版＝🔴 検出）。
    `RegisterPCVersion` で PC(agent)版を `pcs/{pc}.cm_version`（idle PC も）。
    web `/api/version`（最新 Release tag 10分キャッシュ・seam）/`/api/devices`
    に cm_version、devices.js が 🟢/🔴 バッジ＋診断パネル（window_name=
    タイトル等）。**Phase2 遠隔命令**: `commands/{pc}/q` チャネル
    （WatchWake 同系・claim transaction で二重実行防止・Ack 監査・
    near-$0）。`POST /api/command`（owner cookie＋POST 限定＋allowlist＋
    requested_by）/`/api/commands`(監査)、devices.js「再起動/更新/履歴」
    （実行前 confirm）。agent CommandRunner が多層 revocation 再検査→
    dispatch→Ack（破壊的命令は **Ack 先行**＝kickstart -k 自己 SIGKILL で
    監査消失回避）。restart-agent=launchctl kickstart -k 両デーモン、
    self-update=selfupdate.Update→再起動（**手動全機更新ゲートチャ解消**）。
    **Phase3 restart-proxy**: `agent.ProxyRestarter` が sid→kill→
    `proxy --resume <uuid>` detached 再起動（**UUID 鍵のみ**＝pid- は不可
    で web 非表示＋拒否し無関係 kill 防止）。claude --resume 自体は既存
    resume-burst fixture/display-oracle で担保。実行系(launchctl/kill/
    spawn/update)は全 seam＝実 Firestore エミュレータ＋fake seam で
    決定論検証（合成なし）。⚠注意: バイナリ/config はプロセス起動時のみ
    反映＝self-update/restart 後も対象は再起動で初めて新版。
  - 稼働環境 cutover 実施済: proxy alias→Go、monitor→Go launchd
    （`~/Library/LaunchAgents/com.4noha.claude-master.plist`、KeepAlive
    自動復帰検証済）。Python 版は新規不使用（rollback 手順は会話/.bak）。
  - **Python 版との機能 parity 到達（M5e まで完了）= cutover 解禁。**
    cutover（claude-wrap/launchd を Go バイナリへ実切替）は稼働環境を
    変える不可逆・対外操作のためユーザー明示確認後に実施（それまで
    Python 稼働継続）。手順は「移行カットオーバー手順」節参照。
  - M5 内訳: M5a scanner（ps/lsof・実環境 5 セッション実検出）、M5b tmux
    （実 tmux 隔離セッション CRUD）、M5c socket_client（実 socket→実
    Server→pan を display-oracle）、M5d-1 実行可能 proxy（claude-wrap
    置換＝cutover 中核・実録画ラップ統合検証）、M5d-2 monitor ループ
    ＋start/stop/status＋dashboard（実 scan→実 tmux 同期）。
  - M5e 内訳: M5e-1 使用量 status（extract_usage/is_active＋
    _maybe_write_status、実録画で is_active 検証・usage は実 negative）、
    M5e-2 limit_watcher/resume_scheduler＋RunLoop 完全配線（実 status
    スキーマ・実 reset 形式・実 unix socket・実 tmux で中断/再開検証）、
    M5e-3 dashboard.py render 完全移植（実スキーマで枠整合・全要素）。
  - M4 内訳: M4a `IsLiveResetKey`/`ClassifyWheel`（Python 厳密ケース同値）、
    M4b `Server.HandleHostInput`（host nav/pagekey/wheel 状態機械。Python
    `_handle_host_stdin` と判定順含め 1:1、実 VT＋display-oracle 検証）、
    M4c `HistoryFlusher`＋`SESSION_LOG`（実 resume-burst 録画を本番
    capture 経路で転写・ANSI 無・dedup 無、`test_session_log.py` と 1:1）。
  - HOST_FLOW_SCROLLBACK は実 Claude で構造的に不完全（Python 既知制約）
    のため Go へは未移植（SESSION_LOG で代替＝描画非依存で安全）。

## C 案完全自動化スタック（v0.2.1+・本 PC 稼働中）

VSCode タブが crash/閉じ で死んでも会話を保つ仕組み。**proxy は
detached spawn 維持**＝親 terminal の SIGHUP 連鎖から独立、attach
client だけ foreground で走る。**proxy 自身は self-update 反映の
前提も壊さない**（毎回新規 spawn で立ち上がる）。

- **`claude` shim → `claude-master start [args]`**
  （`install.sh` で .zshrc/.bashrc に alias を冪等設定。proxy 直起動
  ではなく start 経由で C 案の自動 attach/復帰経路を必ず通る）。
  - 1. cwd 完全一致の live session → `attachAndExit` 直行＝即復帰。
       `findLiveSessionByCwd` は STATUS_FILE (`~/.claude-master.
       status.json`) を読み updated_at 最新を採用。snap.cwd と現
       shell cwd が乖離していたら `restartProxyByKey` で新 cwd へ
       自動 restart-proxy（user が dir 移動した検出）。
  - 2. cwd 完全一致無し かつ cwd 子孫に live session があれば
       `findLiveSessionsInSubtree` で候補列挙→**必ず picker を出す**
       （1 件でも勝手 attach しない＝親 dir での新規開始を阻まない
       不変条件）。Enter で先頭、"0"/"n" で新規 spawn、数値で指定。
       非対話的 (CI/pipe) は `term.IsTerminal` で fallback=-1＝新規
       spawn＝自動化 script から `claude` を呼んでも横入りされない。
  - 3. 子孫も無し → `resolveResumeArgs` が `agent.ResolveClaudeUUID`
       (`~/.claude/projects/-<...>/<uuid>.jsonl` の権威 cwd 突合せ＝
       サニタイズ規則の逆算をしない＝不変条件) で最新 mtime の UUID
       を解決し `--resume <uuid>` を args に注入→`spawnDetachedProxy`
       で新規 proxy 起動→STATUS_FILE 登録待ち 30s→attach。args 非空
       (user 明示指定) は touch せず尊重。
- **`claude-master sessions [--json]`**: 現存 proxy の cwd 一覧
  （PID/uptime/host_out/cli/cwd）。`internal/diag.ListSessions` が
  `~/.claude-master/diag/<pid>.snap` を走査・`isAlive` で死亡 PID
  除外（Sweep と一貫）。proxy/ps に問合せず負荷ゼロ。
- **`claude-master attach <key>`**: STATUS_FILE 介さず `<key>.sock`
  直接接続。STATUS_FILE 不在/壊れ時の救済路。`client.RunByKey`。
- **idle GC**: `connected_clients == 0 && last_disconnect > IdleGCHours`
  で proxy に SIGTERM（`internal/diag.IdleGCSweep`、monitor RunLoop
  から毎 tick）。toml `idle_gc_hours = 4` 既定。v0.2.3 の host_out
  指標ベース判定が**観測者 broadcast で常時 host_out が動く**ため
  機能不全だった真因を v0.2.4 で修正＝接続中 client 数指標へ。
- **dead PID sweep**: runProxy 入口で `internal/diag.Sweep` が
  `<pid>.sock`/`<pid>.status.json`/`<pid>.snap` のうち PID dead の
  ものを削除（VSCode SIGHUP 連鎖や SIGKILL では defer 走らず残骸
  累積する＝2026-05-20 事故では 17 日で 249 件まで肥大）。自分の PID
  は alive 判定で除外＝race-safe。
- **proxy SIGUSR1 生検**: proxy を殺さず goroutine snapshot 採取
  （`internal/diag.NotifyNonFatal`、`~/.claude-master/crash/<pid>-
  <ts>-sig-user_defined_signal_1.dump`）。stuck proxy 診断で
  「Accept loop alive？master pump 待ち？」を**生体のまま**確認可能。

## 観測スタック（launchd 化・本 PC 運用）

VSCode crash/terminal SIGHUP 連鎖からの**独立観測**が必要。`~/.claude-
master/observe/` 配下に 4 系統。

- **launchd plist 4 本**（`~/Library/LaunchAgents/com.4noha.claude-
  master.watch-*.plist`、`RunAtLoad+KeepAlive`＝kill→数秒で復帰実
  検証済）:
  - `watch-host-out` — `python3 scripts/watch-host-out.py` で
    `<pid>.snap` 1s polling → SUMMARY 60s 周期 / BURST 即時。
  - `watch-vscode` — `scripts/watch-vscode.sh` で VSCode 関連 ps を
    30s sampling。**renderer は `wcfg=<window-UUID>` と `rcid=<id>`
    を併記**＝V8 OOM 犯人 window 逆引き可能（type=- 不明プロセスは
    command 末尾 100 文字を `tail=` に付与）。
  - `watch-ips` — `scripts/watch-ips.sh` で `~/Library/Logs/Diagnostic
    Reports` の新規 .ips/.diag を 30s polling。
  - `watch-logstream` — `scripts/watch-logstream.sh` (= `exec /usr/
    bin/log stream --predicate ...`) で kernel/runningboardd の
    terminated/jetsam/EXC_CRASH を抽出。`exec` で直接子になるので
    KeepAlive が確実に効く。
- **plist 4 本は repo 外**（host 個別の絶対 path を含むため）。
  cloud agent plist と同等の運用＝SA 鍵類同様 git 管理しない。
- watcher 自身が VSCode crash に巻き込まれる事故あり（2026-05-21
  朝の 10:11:58 一斉 SIGTERM では host-out watcher も死亡＝2.5h
  観測穴）→ launchd 化で根治。

## VSCode V8 OOM（本 PC 固有・既知）

Mac-Studio 上の VSCode は `Code main` の **V8 JavaScript heap が 4GB
到達して定期 crash**（dump annotation `electron.v8-oom.is_heap_oom`、
`Mark-Compact (reduce) 3811.2 (4001.4) -> 3811.2 (4001.4) MB last
resort; GC in old space requested → Reached heap limit`）。
.dmp は `~/Library/Application Support/Code/Crashpad/completed/*.dmp`。

- 主犯特定済（2026-05-23）: **`ms-dotnettools.csharp`** の **Roslyn
  Language Server**（`Microsoft.CodeAnalysis.LanguageServer`）。
  `visualstudiotoolsforunity.vstuc` (Unity extension) が **C# Dev
  Kit と `.NET Install Tool` に依存**しており、4noha-studio-family
  配下の Unity プロジェクト（4ksplitter_/pan-pan 等の .sln/.csproj）
  を VSCode が自動検出して activate→Roslyn が 700MB+ 占有→main の
  IPC partner として線形リーク。**Disable では止まらない**（extension
  host 内部の watchdog が Roslyn を即再 spawn する。kill しても
  --pipe を新調して復活）。
- 対策: **uninstall + dir を `.bak` rename + VSCode を Cmd+Q 完全
  終了** が必須。再起動で「メモリ上に load 済みの extension code」
  も含めて消える。Unity extension は marketplace から外れる名前
  （`visualstudiotoolsforunity.vstuc` は code CLI 経由の uninstall
  が "not installed" で拒否される）＝ .bak rename + Cmd+Q で実質
  無効化。
- 改善実測: 主犯対策前 **1-2h/回 crash** → 対策後 **5-7h/回**（≒
  9 倍）。ただし残り +6MB/分 の trigger は別途あり：
  - Round 1 (5/24): `saoudrizwan.claude-dev` (Cline) uninstall → 効果
    なし（5/24-29 の crash 頻度ほぼ同じ）＝**Cline は無関係**。
  - 残候補（未検証）: Pylance (ms-python.vscode-pylance) の大規模
    monorepo 解析、もしくは Settings Sync 経由で再生する extension。
- C 案 + UUID resume picker で **crash しても会話は jsonl から復帰
  可能**＝運用上「日に 1 回 VSCode 再起動」で許容できる水準。

## 開発の鉄則（Python 版で何度も破って学んだ）

1. **推測修正をしない。** 「動かない」報告はまず原因を実再現してから直す。
2. **実テストで担保。** 単体ロジックでなく *実キーパス / 実 claude 録画*
   で検証。Python の `~/works/claude‐master/debug/fixtures/*/bytes.bin`
   （特に `resume-burst` = 実 `claude --resume` 録画）を Go 回帰へ流用し、
   Python の display-oracle と挙動一致を機械確認する。合成ストリームだけ
   で緑にしない。修正前に「旧コードでそのテストが落ちる」ことを確認。
3. **キー到達性は `sed -n l` で切り分け。** 端末でキーを押しエコーが
   出れば pty 到達、出なければ上位レイヤ（端末アプリ/Karabiner）が横取り
   ＝アプリ側では直せない。下記「端末キー到達性」参照。

## 不変条件（Python 版から継承・絶対）

- **ヒューリスティック分類は一切しない**（log/footer 推測・キーワード判定・
  lazy emission・内容 dedup）。これが脆さの根。あるのは「忠実な VT
  エミュレート＋ viewport 再描画」だけ。
- **スクロール位置は canvas 先頭（最古）からの絶対アンカー**。最下部基準
  だと遡り中に claude 出力で canvas が伸び表示がドリフトする（実録画で
  確認済の実バグ）。末尾追記で view が動かない＝tmux copy-mode と同じ。
- **quiescence ゲート**: 端末ネイティブ scrollback へ流すのは claude
  静止時のみ。ストリーミング中は確定行を内部に capture するだけ。
- **クリアは `\x1b[2J\x1b[9999;1H`**（全消去後カーソルを画面最下部へ）。
  claude は「カーソル最下部前提で下から描画」するため最上部だと崩れる。
- **カーソル復元（必須）**: `RenderANSI` はフレーム末尾（`\x1b[?2026l`
  直前）で `\x1b[<行>;<桁>H` を出し物理カーソルを VT モデル位置
  (`v.cy/v.cx`) へ戻す。欠くと最終行末尾（≒右下）に残り IME preedit が
  そこに出て**日本語入力が事実上不能**（半角直接入力は preedit 非表示で
  露見しにくいだけで同一不具合）。桁は `draw()` が runewidth で進める
  **表示桁**（rune 数で数えない＝全角で半分ずれる）。viewport 先頭絶対行
  は `ScrollRenderer.lastOy`、行 = `len(hist)+cy-lastOy+1`。nav 遡りで
  カーソルが viewport 外の時は出さない（読書中・IME 非使用）。回帰検知:
  `internal/screen/cursor_test.go`（半角/全角/遡り外）。
- **フレーム中の cursor 不可視化（必須）**: `RenderANSI` は BSU 直後に
  `\x1b[?25l`、cursor 復元できる場合のみ ESU 直前に `\x1b[?25h` を出す。
  理由: DECSET 2026（同期出力）が tmux→外側端末まで完全伝搬しない経路
  （tmux 3.6 + VSCode terminal 等。`xterm-256color` terminfo に Sync
  capability 無し）では `\x1b[2J\x1b[9999;1H\x1b[H` + 各行描画の間
  カーソルが各位置で可視のまま描かれ「カーソルが散ってちらつく」事象に
  なる。VT モデルは DECTCEM (`?25h/l`) をセル非影響として無視する
  （`vt.go csi`）ので claude 意図と衝突しない（そもそも proxy frame に
  載っていない＝Web も同症状で `sync.js` 投入の前例あり）。nav scrolled
  -off は hide のまま ESU（cursor 不要・次フレーム live 復帰で自動 show）。
- claude --resume は `\x1b[2J` せず絶対座標で会話を再ストリーム→ pyte/VT
  が同内容を複数回スクロールし history が重複。dedup は禁手なので
  ファイル転写（SESSION_LOG）か `SIZE_POLICY=host` 生パススルーで対処。
- **モード**（`SIZE_POLICY`、既定 `client`）: 設計上は client / host
  (生パススルー) / largest / smallest / latest。
  **⚠ Go 実装の現状（重要・Python と差異）**: proxy が分岐するのは
  `SizePolicy=="host"`（生パススルー）**のみ**。`client/largest/
  smallest/latest` は未実装で、config パース以外に効果が無く全て
  同一挙動になる。具体的には:
  - PTY/claude サイズを変えるのは `run.go` の **host SIGWINCH のみ**
    （`p.Setsize()` の唯一の呼び出し元）。host = `claude-master proxy`
    を起動した端末。
  - tmux `socket-client`・Web 等の client が送る RESIZE は
    `server.go` で **その client 自身の per-client ビューポート描画
    サイズ**を決めるだけ（`c.rows/c.cols`→`renderClientLocked`）。
    PTY/claude/他 client には一切影響しない（＝独立ミニ tmux）。
  - したがって Python 設計の「最後に/最大の client へ PTY 追従」は
    Go には無い。`size_policy="largest"` 設定でも実質 client と同じ。
  この差異を `client/largest` で PTY が動く前提で語らないこと
  （Web RESIZE 誤診の温床になった）。実装するなら server.go に
  client サイズ集約＋`p.Setsize` 経路を新設する必要がある。

## 端末キー到達性（Mac JIS / VSCode / Terminal.app）

nav-mode/PAGEKEY/WHEEL は「キーが pty まで届く」のが前提。届かない事象は
アプリ側では直せない。切り分けは常に `sed -n l`。

- **PageUp/PageDown/Home/End**: 端末アプリが既定でスクロールに割当て pty
  に送らない。対処は端末側設定:
  - VSCode `~/Library/Application Support/Code/User/keybindings.json`:
    `workbench.action.terminal.sendSequence` で `[5~`/`[6~`
    (when: terminalFocus)。
  - Terminal.app: 設定→プロファイル→キーボードで `page up`→「テキスト
    送信」`\033[5~`（Esc キーで `\033`）、`page down`→`\033[6~`。
- **矢印/文字/`Ctrl-\`(=\x1c)** は素で届く ⇒ nav-mode は設定不要で確実。
- **JIS keycode 実測**: `_`=`international1`(Ctrl→`\x1f`)、ANSI`\`=
  `backslash`(→`\x1d`)、`¥`=`international3`(→**`\x1c`** ✅)。`Ctrl-_`で
  NAV_KEY(`\x1c`)を出す Karabiner: `from international1`(mandatory
  control,optional shift)→`to international3`+`left_control`、端末/VSCode
  に scope。素シェルで `\x1c`=SIGQUIT だが raw モードの proxy には届く
  （検証は claude 内で）。
- claude は `?1004h`(focus)/`?2004h`(bracketed paste) を有効化 ⇒ 端末が
  `\x1b[I`/`\x1b[O` 等を自発送出。scroll の live 復帰は **実ユーザー操作
  のみ**で行い、focus/mouse/デバイス応答等 passive では戻さない
  （`is_live_reset_key` 相当を Go でも実装）。

## アーキテクチャ（Python → Go）

| Python | Go pkg | 状態/備考 |
|--------|--------|-----------|
| config.py | `internal/config` | ✅ M1。env > ~/.claude-master.toml > 既定。NAV_KEY パーサ移植・実 toml で Python と全項目一致 |
| pty_scroll.py / pty_emulator.py | `internal/screen` | ✅ M2(山場)。自前 VT モデル（vt10x/x/vt はデータで棄却）＋ history.top ＋ 先頭アンカー ＋ render_viewport。✅ M4c HistoryFlusher/line_to_text/IsLiveResetKey/ClassifyWheel |
| pty_proxy.py | `internal/ptyproxy` | ✅ M3 creack/pty fork+exec＋unix socket 多重化＋RESIZE/SCROLL。✅ M4b `HandleHostInput`（host nav/pagekey/wheel）＋ M4c `SESSION_LOG` |
| socket_client.py | `internal/client` | ✅ M5c unix socket + x/term raw・client 側 nav/pagekey/wheel（分類器は M4 共有）・実 socket→Server pan を display-oracle 検証。✅ クリップボード・ブリッジ（tmux/端末→リモート画像貼付）: `IMG_PASTE_KEY`/toml `img_paste_key`（既定 off・nil）。設定キー押下で macOS のクリップボード画像を osascript で取得し term.js と同一 IMAGE フレーム(`0xff 0xfd|u32 len|u8 code|payload`)を送出＝**サーバ無改変**で `handleImagePaste` 経路を再利用。画像無し時はキーを素通し（Ctrl-V 通常動作維持）。`readClipImage` seam で実 GUI 非汚染テスト。実キーパス e2e（実 socket→実 Server→setClip）＋ manual タグの実クリップボード回帰で検証。**⚠診断（誤診注意）**: 画像不着で claude が「No Image Found」/無反応＝IMAGE フレーム未生成で bare Ctrl-V が通っただけ＝**送出側（その機）に確定**（旧バイナリ／`img_paste_key` 未設定／cloud attach が GUI(Aqua) 外で osascript 不可／更新後セッション未再起動 のいずれか）。根拠: `handleImagePaste` は osascript 成功時のみ 0x16 注入＝届いていれば添付成功し No Image にならない＝**転送路（relay/Cloud Run/agent）は無実**。relay の `coder/websocket` は `NetConn` が netconn.go で `SetReadLimit(-1)` 済＝「relay 32KB 読取制限が原因」は誤診（ここを直すと非バグ破壊）。バイナリ/config はプロセス起動時のみ反映＝update/toml 変更後はその機の cloud attach/tmux を要再起動 |
| process_scanner.py | `internal/scanner` | ✅ M5a ps/lsof・実環境 5 セッション実検出。**lsof ロケール注意**: launchd は LANG/LC_* 未設定＝C ロケールで lsof が非ASCII cwd を文字列 `\xNN` に化かす（U+2010 ハイフン等を含むパス破損）。`getCwdLsof` は UTF-8 ロケール強制＋`unescapeLsof` の二重防御で実バイト復元 |
| tmux_manager.py | `internal/tmux` | ✅ M5b 実 tmux 隔離セッション CRUD |
| monitor.py | `internal/monitor` | ✅ M5d-2 scan 差分→tmux 同期＋start/stop/status＋最小 dashboard。limit_watcher/resume_scheduler は M5e |
| pty_proxy.py main/run/_loop | `internal/ptyproxy` (RunProxy) | ✅ M5d-1 実行可能 proxy＝claude-wrap 置換（cutover 中核）。使用量 status は M5e |
| debug/ replay+display-oracle | `test/` + 流用 fixtures | 全 M で実録画回帰 |
| （新規）クラウド同期 | `internal/sync` 等 | M6。FCM wake + Cloud Run WSS + Firestore（DESIGN.md）|
| （新規）C 案完全自動化 | `cmd/.../start.go` + `internal/client.RunByKey` + `internal/cloud/agent.{ProxyRestarter,ResolveClaudeUUID}` | ✅ v0.2.1+。`claude` shim 経由で start→cwd 一致 attach / cwd 子孫 picker（1 件でも必ず picker＝親 dir 新規開始を阻まない）／ jsonl 最新 UUID 自動解決→`--resume` 注入で完全自動会話継続 |
| （新規）sessions サブコマンド | `internal/diag.ListSessions` + `cmd/.../sessions.go` | ✅ v0.2.6。`<pid>.snap` 走査で現存 proxy 一覧（PID/uptime/host_out/cli/cwd）。proxy/ps 非問合せ＝負荷ゼロ |
| （新規）idle GC | `internal/diag.IdleGCSweep` + `monitor.RunLoop` | ✅ v0.2.4。connected_clients==0 + last_disconnect > IdleGCHours で proxy SIGTERM（v0.2.3 の host_out 指標は observer broadcast で動き続け機能不全＝指標を真に切替） |
| （新規）SIGUSR1 生検 | `internal/diag.NotifyNonFatal` | ✅ 殺さず goroutine snapshot 採取＝stuck proxy 診断（Accept loop alive？master pump 待ち？を生体で判定可） |
| （新規）dead PID sweep | `internal/diag.Sweep` | ✅ proxy 起動時に dead PID の sock/status/snap を削除（VSCode SIGHUP 連鎖や SIGKILL の defer 走らず残骸累積を根治） |
| （新規）観測 watcher launchd 4 系統 | `scripts/watch-{host-out.py,vscode.sh,ips.sh,logstream.sh}` + 4 plist | ✅ 2026-05-23。VSCode crash 連鎖から独立。renderer args 含む V8 OOM 犯人 window 逆引き対応 |

`internal/selfupdate` + `claude-master update`、`install.sh`、
`.goreleaser.yaml`、`Makefile` は実装済（配布: 完全静的・CGO 不要・
darwin/linux × amd64/arm64・asset 名 `claude-master_<os>_<arch>` +
`checksums.txt` を install.sh/selfupdate/goreleaser/make dist で一致）。

## 配布 / 更新

- unix/macOS ワンライナー: `curl -fsSL https://raw.githubusercontent.com/4noha/claude-master-go/main/install.sh | sh`
- **Windows: `install.ps1`（M8g 実装。冪等・`-DryRun`・自己昇格）**。
  Windows Release asset 未発行のため当面は repo clone 後ソースビルド:
  `pwsh -ExecutionPolicy Bypass -File install.ps1 -Build`（既存 exe
  流用は `-Source <exe>`）。claude シム＋User PATH／ASCII `*.cmd`／
  toml 雛形／S4U タスク monitor・cloud（要管理者＝UAC 自己昇格、
  `-SkipTasks` で回避可）。前提物（実 claude.exe／psmux／sa.json）は
  検出して案内（自動生成不可）。Windows Release 発行後は
  `irm https://raw.githubusercontent.com/4noha/claude-master-go/main/install.ps1 | iex` も有効。
- 更新: `claude-master update`（sha256 検証・原子置換）or 同スクリプト
  再実行（unix=install.sh / windows=install.ps1・冪等）
- リリース: `git tag vX.Y.Z && git push --tags`（goreleaser CI 化は未）
  or ローカル `make dist`。**実 DL 経路は Release 発行後に有効**
  （goreleaser の windows/amd64・arm64 asset 追加も follow-up）。

## ビルド / テスト

```sh
export PATH="/opt/homebrew/bin:$PATH"   # Homebrew Go
go test ./... && go vet ./...
make build VERSION=$(git describe --tags --always)
make dist                               # 配布物 + checksums.txt
```

## GCP クラウド同期（M6・設計確定済）

push-wake / 差分時のみ / NAT 越え。FCM(無料,wake) + Cloud Functions
(scale-to-zero,差分トリガ) + Cloud Run(min0,WSS データ線) + Firestore
(状態)。常時 VM ゼロ・課金は実同期量比例で小規模ほぼ$0。「差分なし＝
データ線切断」は quiescence 判定を再利用。詳細・コスト表は DESIGN.md。

## 移行カットオーバー手順（Go が M5 検証通過後）

1. `make dist` → Release 発行（or install.sh 経由で配置）。
2. `~/.claude-master.toml` はそのまま（キー互換・parity 済）。
3. `claude` エイリアス/`claude-wrap` を Go バイナリ呼び出しへ差し替え。
4. launchd plist の監視デーモンを Go の monitor へ差し替え、旧 Python
   デーモン停止。
5. 実 claude セッションで nav/pagekey/wheel/scroll/同期を実機確認。
6. 問題あれば即 Python ベースへロールバック（Python は当面残置）。
