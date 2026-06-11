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
  - **relay 再接続 takeover 修正 ✅（2026-06-11・Cloud Run rev 00033）**:
    Web「表示が壊れる」（正しい transcript に別時点 footer 断片が
    スプライスされた合成画面のまま凍結）の真因は relay `serve` が同 sid
    再接続（タブ再読込/‹›コンソール切替/agent 再接続）で slot を黙って
    上書きし pump を並走させる構造: ①同じ source を 2 つの io.Copy が
    read→32KB chunk 奪い合い＝新 viewer の stream に歯抜け（frame 中間
    欠落） ②旧 pump 終了 cleanup が現役 conn を巻き添え close＝凍結
    ③双方 pump の close(done) 二重実行 panic＝**relay コンテナごと落ち
    全セッション切断**。conn 毎に読み手 1 つ（serve 自身が唯一の
    reader）＋書き先はロック下で現役 slot 解決、へ書換。診断手順:
    **proxy sock を Web と同条件（RESIZE 500×160）で直採取**し server
    frame 健全（102/102 atomic・hist 汚染なし・deployed static=repo
    一致）を先に証明→消去法で relay 層に絞り、実 WSS テストで panic
    再現→修正（`TestViewerTakeoverKeepsStreamIntact`）。実 GCP e2e
    （`go test -tags manual -run TestE2ERealGCP`、要 GCP_PROJECT/
    CLOUD_RELAY_URL/GOOGLE_APPLICATION_CREDENTIALS。M7 Grant 仕様
    追随済）で新 revision 検証緑。⚠Web 知見: 160×500 viewport は
    1 frame≈108KB×spinner tick 12.75fps＝**1.35MiB/s**（クライアントが
    遅いと proxy 側 2s write deadline で切断）。バンドル xterm.js は
    DECSET 2026 非対応のまま＝sync.js の 1 emit=1 frame が原子性の砦
    （xterm.js `_innerWrite` は chunk 単位同期 parse＝1 write 内は
    paint 不可と minified 実コードで確認済）。
  - **M9 Web Firestore 更新 push ✅（2026-06-11・rev 00034）**: quiescence
    切断（無通信 30s・near-$0 設計）後の Web を**ネイティブ
    WatchSessions と同型の push で自動復帰**させる。relay `/api/fbtoken`
    （owner cookie 必須）が SA 鍵（ENROLL_SA_JSON_B64 既存）署名の
    Firebase custom token を発行＝**identity は全端末共通 uid=cm-owner**
    （単一オーナー設計・SA 鍵は渡さない）。term.js が
    `pcs/{pc}/sessions/{sid}` を onSnapshot し、status 変化（is_active
    等＝content_hash ゲート）で切断中なら自動再接続（純関数
    `cmReconnectGate`: CONNECTING/OPEN 中は張らない・1s×2^n backoff
    上限 30s）。キー/画像入力も queue→再接続で送出＝タイプで線が開く。
    アイドル中は接続ゼロ＝Cloud Run 温まらない（near-$0 不変）。
    **GCP 一回設定済**: Firebase 有効化・Web アプリ登録（公開 apiKey）・
    Identity Toolkit initializeAuth・firestore.rules v2 release
    （cm-owner=pcs/** read-only・他全拒否。サーバ SDK は rules 非対象＝
    ネイティブ同期無影響。deploy は firebaserules REST）。relay env に
    `FIREBASE_WEB_CONFIG_B64`（公開 config JSON の b64。未設定なら
    /api/fbtoken 404＝push 無し従来動作）。検証: 実 RSA 鍵 mint→RS256
    機械検証／実 Identity Toolkit 交換→実 Firestore rules read 許可・
    write 拒否・pcs 外拒否（`-tags manual` `TestFBTokenRealExchange
    AndRules`、要 FIREBASE_API_KEY）／gate は出荷 term.js 抽出の
    `reconnectgate_test.mjs`。⚠sync シムは**接続ごとに新規**＋全 WS
    ハンドラに現役 identity ガード（旧 conn の遅延イベント遮断＝relay
    takeover 修正と同じ規律のブラウザ版）。
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

## 一層目ダブルバッファ（v0.3.1・claude の DECSET 2026 honor）

- **実 claude は再描画を ?2026h..?2026l で括る**（実録画 90KB 中 35 対・
  最大 73KB）。proxy はこれを frame 放送の保留判定に使う: `vt.go` が
  2026 を状態追跡（セル非影響は不変）、`server.go masterPump` が
  **SyncActive 中は放送保留・ESU を含む read で完成状態を 1 frame**。
  「チカチカは消えたが再描画ブロックが見える」（2026-06-11 実報告）の
  根本対応＝転送のアトミック性に加え**意味のアトミック性**を獲得。
- 安全弁 3 つ: ①ESU 無しで 1s 超→read 毎放送に復帰 ②read も無い停止
  → AfterFunc タイマーで 1 度放送 ③master EOF→保留 flush して停止。
  RESIZE/SCROLL/attach catch-up は非ゲート（即応・次 ESU で収束）。
- 回帰: `syncgate_test.go`（実録画の実 sync 区間を実 PTY 分割供給・
  旧コード FAIL 確認済）/`TestVTSyncActiveTracking`。録画末尾が BSU で
  閉じない（実録画がそう）ケースは EOF flush/valve が担保。
- ⚠バイナリはプロセス起動時のみ反映＝**各セッションは proxy 再起動で
  初めて v0.3.1**（Web 再起動ボタン or restart-proxy）。

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

## STATUS flap / start 30s timeout（2026-06-12 解決済）

- **症状**: `claude`（start 経由）のセッション復元が「start: 30s 待っても
  session が STATUS_FILE に出ない」で失敗、開発が進むごとに悪化。
- **真因連鎖（read-only 調査→懐疑検証 2 レンズで実測確定）**: launchd
  plist の `ProcessType=Background` → monitor と exec 子が pri=4（通常
  31）に throttle → 子 `ps aux` が 8-12s（前面 0.29s・`taskpolicy -b`
  再現 77.9s）→ `scanner_unix.go` の 10s timeout を確率超過 → RunLoop が
  エラー握り潰し**空 sessions で STATUS 全置換**（59B flap・観測窓の
  76% が空・最長連続 50s）→ start の `waitKeyForCwd`（cwd 文字列一致
  のみ・500ms poll）が 30s 窓に非空書込ゼロで timeout。**正帰還×2**:
  ① start は spawn が wait より先＝失敗 exit で同 UUID dup proxy が残置
  （実測 sock 12→15/1h・UUID 4728811a×4 等・claude 計 16 本）→ scan N
  増（throttle 下 lsof 2-6s/件）→ tick 11→34→100s ペースへ悪化
  ② launchd Program が repo build 成果物そのもの → `make build` の
  in-place 上書きで稼働 daemon が OS_REASON_CODESIGNING SIGKILL
  （launchctl runs=12）→ KeepAlive 再起動 churn・旧 inode 常駐混在。
- **反証済（誤診注意）**: /Volumes デッドマウントで lsof 張り付き説
  （前面 0.07s/件＝QoS が真因）／dup UUID で待ち永久不成立説（判定は
  cwd 一致＝非空 STATUS 1 回で成立）／「monitor 未稼働」説（稼働した
  上で空を書いていた）／59B=破断読み説（正規の空 JSON 書込）／idle-gc
  PID 再利用で monitor 自殺説（ログに痕跡ゼロ。`4h0m0s ago` 同値は毎秒
  sweep の閾値跨ぎ仕様＝正常）。
- **修正（コード 3 点・回帰テスト付き）**: ① RunLoop の scan エラー
  tick は **skip＝前回状態維持**（STATUS 全置換も全 RemoveWindow も
  しない。`TestRunLoopScanErrorKeepsStatusAndWindows`＝旧コード FAIL
  確認済・seam `scanSessions` でエラー注入） ② `WriteStatus` を
  tmp→rename 原子化（truncate 直書きの 0B 瞬間を実観測） ③ start に
  **STATUS 非依存の dup 防止 backstop** `findLiveManagedByUUID`
  （scanner 直叩き＋`<pid>.sock` 存在で同 UUID live を検出→spawn せず
  sock 直接 attach→死んでいたら RunByKey fallback。30s timeout 時も
  同 backstop で spawn 済み proxy へ直接 attach＝孤児/dup の種を残さ
  ない。`TestFindLiveManagedByUUID`）。
- **修正（運用 2 点・plist は repo 外）**: ① 両 plist
  （monitor/cloud）の `ProcessType=Background` 削除（pri 4→20＝前面
  同等速度。修正後実測: STATUS 7 件安定・更新 1-2s 間隔・空ゼロ）
  ② launchd Program を `~/.claude-master/bin/claude-master` へ分離＝
  **開発ビルドが稼働 daemon を SIGKILL しなくなった**。
- **⚠新・運用手順（重要）**: デーモンへの反映は `make build` だけでは
  起きなくなった。`rm ~/.claude-master/bin/claude-master && cp
  claude-master ~/.claude-master/bin/`（**rm→cp＝新 inode 必須**・
  macOS 署名キャッシュ罠は tmux 差替と同類）→ `launchctl kickstart -k
  gui/$UID/com.4noha.claude-master`（cloud も同様）。proxy/shim は
  従来通り＝新規 spawn から新版。
- 検証: 全 18 pkg `go test` 緑・3-OS build 緑／実 e2e（scratch dir で
  start）＝spawn→STATUS 登録→「セッション pid-N に接続」が数秒で成立
  ／dup proxy 8 本整理（UUID 毎 1 本・client 接続中は保持）。
- 既知 quirk（未対処・実害小）: start の cwd 一致は文字列比較＝symlink
  経路（`/tmp`→`/private/tmp` 等）は不一致で 30s timeout になる。実
  プロジェクトの `/Users/...` では非発生。対処するなら EvalSymlinks
  正規化（scanner は物理 path を返す）。
- **余波: tmux 窓の頻繁な削除再生成（同夜 第二真因・解決済）**: plist
  修正後も「活動中セッションの窓だけ 1-2 分周期で死亡→heal 再生成」が
  継続。真因＝**tmux サーバが旧 Background monitor の子として spawn
  され darwinbg を継承**（pri=4。plist 修正は新規プロセスにのみ有効・
  `taskpolicy -B` でも解除不能と実測）→ 高負荷時に pane pty を 2s 内に
  drain できず proxy の write deadline（`renderClientLocked` 2s）が
  **その窓の client だけ選択的に切断** → socket-client は conn close
  1 回で exit する設計（「--- Claude session ended ---」exit 0）＝窓
  死亡 → heal 再生成。idle セッションは broadcast が稀で無事＝「活動中
  だけ churn」の偏りが診断の鍵。**診断手法**: 窓に `remain-on-exit on`
  を一時設定し dead pane の exit status/stderr を capture-pane で回収
  ＋同 proxy へ手動 client（`script -q` の pty 付き）を並走させ
  「proxy は無実・pane 経路のみ死ぬ」を分離証明＋`window_id` 追跡で
  churn を機械検出。**修正**: `tmux kill-server` → monitor kickstart
  ＝新 monitor（非 throttle）が新サーバを spawn（pri 4→20）。全 14 窓
  （local 7＋remote 6＋dashboard）は自己治癒で数秒再生成・proxy/claude
  無傷。**教訓: launchd の ProcessType 変更後は、旧デーモンが spawn
  した長命の子（tmux サーバ等）も作り直すこと**（darwinbg は子へ遺伝
  しプロセスが生きる限り残る）。設計メモ（未対処）: socket-client は
  「write deadline 切断」と「セッション終了」を区別できない＝将来
  同種の遅延があると窓 churn として再発し得る（対処案: socket-client
  の自動再接続 or deadline 緩和。QoS 根治済のため今回は見送り）。

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
- **ちらつき防止の層モデル（重要）**: BSU/ESU (`\x1b[?2026h/l`) は
  「層内アトミック commit」のプロトコル。proxy→tmux→外側端末の **3 層**
  あると 1 段ごとに sync 宣言が要る（透過に連鎖しない＝tmux は受信した
  BSU/ESU を消費して自分の redraw に作り直す）。守備は:
  - **proxy→tmux 区間**: proxy frame の BSU/ESU + 本項目の `?25l/h`。
    tmux 3.4+ は BSU/ESU を理解して内側 redraw を ESU まで遅延し、
    `?25l/h` を内側 VT 状態に反映 → tmux 自身の外側 redraw も「cursor
    不可視で cell 更新→最後に可視」になる。
  - **tmux→外側端末区間**: tmux は外側 redraw を sync mode で囲むか
    どうかを **terminfo の Sync capability または `terminal-features`**
    で判定。標準 `xterm-256color` terminfo に Sync 無し＝裸ストリーム
    ＝cursor 移動が露出する。対処は `~/.tmux.conf` に
    `set -as terminal-features ',xterm*:sync'`（VSCode terminal/iTerm2/
    最近の xterm.js 系はサポート済＝外側端末で atomic 描画）。
  - host 直接（tmux 非経由）は 1 段なので proxy frame の BSU/ESU だけで
    完結（=ホストでちらつかない理由）。Web 経路は `cloud/web/static/
    sync.js` が xterm.js 用 BSU/ESU shim として同じ役を果たす。
  - 視覚的に「cell 更新は人間に incremental に見えにくいが cursor は
    overlay で即時追従＝tick より速く 1 frame に複数位置で描かれる」
    のがちらつき体感の正体。`?25l/h` だけでも cursor は止まる、`,sync`
    も併用すれば cell も atomic（推奨）。
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

## tmux 経路の既知制約と運用 trap

tmux を間に挟むと「中間 VT＋外側端末」の 2 層構造になり、host 直接では
出ない種類の事故が出る。発覚順に集約。

- **`tmux new-window` の silent fail（2026-06-05 解消）**: オプション無し
  `new-window -t <session>` は active window の index に作ろうとし、衝突
  すると `create window failed: index N in use` で **exit=1**。`tmux.go`
  の `out()` は `exec.Command(...).Output()` で stderr を捨てるため、
  監視ログにも一切痕跡が残らず**沈黙して失敗**する。実害: monitor
  restart しても外的に kill された窓が再生成されない（known map が key
  を覚えており AddWindow は呼ぶが new-window が失敗、ループ毎に同じ事を
  silent に繰り返す）。修正は `internal/tmux/tmux.go` の new-window 全 4
  箇所（SetupDashboard/EnsureCmdWindow/NewMarkedWindow/AddWindow）に
  **`-a` を追加**＝active window 直後に挿入させ tmux 自動の index 衝突
  回避を効かせる。base-index=0 / renumber-windows=off の実環境で再現
  確認。今後 tmux 系コマンドを足す際は `outErr` で error 拾うか
  `CM_DEBUG` で stderr を残す方が事故察知が早い。
- **monitor の `known` map は外的 kill を自己治癒しない（未対処）**:
  `monitor.go RunLoop` は新規キー時のみ AddWindow し、`known[key]` 既存
  なら status update のみ。user が `prefix+&` 等で窓を手動 kill しても
  monitor は known を信じ続け再生成しない。`-a` 修正後は新窓作成自体は
  成功するので「monitor restart で復帰」は可能になったが、restart 無しの
  自己治癒は未実装。修正案: RunLoop の current ループで `mgr.WindowFor
  (key) + ListWindows` 突合せて窓 missing なら再 AddWindow に流す。
- **nav-mode は silent toggle で UX 罠（未対処）**: NAV_KEY（既定
  `\x1c`=Ctrl+\、JIS は Karabiner で Ctrl+_ も）押下で `client.go
  processStdin` が `*navMode = true` にトグル → 黄色 `[NAV MODE ON]` を
  stdout 出力 → 以降スクロール系以外の全キー握り潰し（`return false,
  nil`）。claude へは `ReplaceAll(data, navKey, nil)` で剥がれて到達
  しない＝proxy も無関知。**症状**: 「タイプしても反応ない＝壊れた窓」
  と user が誤判定し手動 kill → 上記 known バグで再生成されない連鎖。
  メッセージは出るが claude ストリーミング中だと一瞬で流れ、cursor
  ちらつき（解消済）で画面ノイズに紛れる。復帰は単純に Ctrl+\ もう
  一度（トグル）。改善案: nav-mode 中は status-line / window-name に
  `[NAV]` 表示／tmux pane title などで持続的な視認サイン。
- **既存 client は terminal-features の再評価をしない**: `tmux show-
  options -gv terminal-features` を server 単位で更新しても、attach 済
  client の `client_termfeatures` は attach 時点の解決値を保持。新 sync
  capability を有効化したいなら detach→再 attach 必要（`tmux detach-
  client -a` で session/窓無傷、user が再 attach）。
- **tmux outer 出力の sync wrap は完全でない（2026-06-05 実測）**: `set
  -as terminal-features ',xterm*:sync'` 宣言済 ＋ client_termfeatures に
  sync 含む を確認しても、`script -q` で生バイト録画すると **約 50% が
  BSU/ESU 外で裸 stream として emit** される（10s capture で 11 BSU/ESU
  ペア・atomic 区間 9490B vs 裸 7753B）。裸 chunk は 1800-3200B 単位で
  CUP+SGR+cell 並ぶ全行更新で、scroll 時 flicker の真因。tmux 3.6 の
  sync wrap が pane redraw 単位では張られても、status-line refresh や
  特定 redraw path で漏れる挙動。**proxy 側 frame は完璧 atomic
  (`?2026h+?25l+...+?25h+?2026l` hex 確認済) で源は tmux 側**。
- **DECSET 2026 を honor する端末（DECRQM 実測で判定すること）**:
  - iTerm2 3.4+: ✅ native 対応（documented・**2026-06-12 実機確認**:
    proxy v0.3.1＋パッチ tmux 経由の実 claude 窓でちらつき無しを
    ユーザー確認＝Terminal.app ❌ 判定の対照実証）
  - kitty/alacritty/WezTerm: ✅ native 対応（documented）
  - **VSCode terminal: ✅ 認識する（2026-06-05 本 PC 実測。DECRQM
    `CSI ?2026$p` → `CSI ?2026;2$y`＝Ps=2 認識済）**。⚠過去の調査で
    「xterm.js は 2026 非対応（sync.js が書かれた理由）」と**未検証の
    まま断定して L4 設計を狂わせた誤情報**。Web の sync.js は古い
    bundled xterm.js 向けで、VS Code 本体の xterm.js は対応済み。
    端末対応は推測せず `scripts/probe-term-sync.py`（DECRQM・目視不要）
    で**機械判定する**こと。
  - **Mac Terminal.app: ❌ 非対応（2026-06-12 本 PC 実測）**。DECRQM
    自体に NO-REPLY（DA1 は応答＝probe 有効）。実害も確認済: proxy
    100% atomic＋パッチ tmux naked 0%＋sync feature 適用済の完璧な
    ストリームでも Terminal.app が括りを無視して逐次描画＝ちらつく。
    **tmux 閲覧に Terminal.app を使わない**（VSCode terminal/iTerm2 等
    を使う）。tmux-wrap/tmux-render も 2026 honor 前提なので救えない。
  - 帰結: VSCode terminal で bare tmux が flicker する真因は端末では
    なく **tmux outer の 64% 裸 emit（m1 実測）**。`tmux-wrap` の
    sync-wrap（flush を 100% BSU/ESU で囲む）で構造的に塞がる。

## tmux 経由ちらつき: ⚠一部再発（next-3.7 MODE_SYNC の wrap 漏れ）

**⚠訂正 (2026-06-11 夜・実測)**: cutover 後も claude 窓の素 attach は
ちらつくと実報告→再計測で **tmux master (HEAD-86128a7) の pane-2026
(MODE_SYNC) 実装が redraw 本体を outer sync wrap の外へ漏らす**バグを
確定。「✅根本解決」の A/B (naked 2%) は**再現しない**（誤計測の疑い:
下記「窓の取り違え」trap。pane が 2026 を使わない窓は 99% wrapped に
なるため、carbonyl 等の窓を測ると健全に見える）。
- **決定的データ（隔離 tmux サーバ＋合成 2026 producer・12fps）**:
  1 frame ごとに `[6B naked][48B wrapped: scroll+cursor のみ][3600B
  naked: 行再描画の本体!][1337B wrapped]` ＝ **naked 72%**。実 claude
  窓では naked 13%（本文テキスト・✻ 行・`[4;59H[12;68H...` の naked
  カーソル移動連発＝カーソル散りの正体）。**2026 honor 端末でも漏れ分
  は即描画＝全端末でちらつく**（VSCode terminal は test4 目視で honor
  確定済＝端末は無実）。upstream master に修正コミット無し (6/11 時点)。
- **緩和策（実測検証済）**: `claude-master tmux-wrap -- tmux attach -t
  claude-master` ＝ **naked 0.0%・flush が producer frame と 1:1 整列**。
  3.6a 時代に「時間基準では frame 整列不能」だった批判は、next-3.7 の
  出力が **frame ごとの burst**（〜80ms 間隔 ≫ idle 4ms）になったため
  解消＝idle batch が frame 境界と自然に一致する。連続飽和ストリーム
  時のみ MaxHold(50ms) 境界が frame 中に落ち得る（既知の限界・実
  workload 12.75fps では非発生）。`tmux-render` も従来どおり完璧
  （ユーザー実機確認済）。
- **診断 trap「窓の取り違え」**: `tmux attach` での capture は
  **session の current window** を録る。狙う窓を確実に録るには
  `tmux new-session -t <session> -s capdiag`（grouped session＝current
  window 独立・ユーザー表示を妨げない）→ `select-window` → attach。
  pane が 2026 を使う窓（claude）と使わない窓（シェル等）で tmux の
  出力経路が全く違うため、**違う窓を測ると逆の結論が出る**。
- **upstream 修正 PR 提出済: https://github.com/tmux/tmux/pull/5195**
  （2026-06-11・fork `4noha/tmux` branch `fix-sync-update-tty-leaks`）。
  -vv ログ追跡で**漏れは 3 経路**と確定し全て修正:
  ① ESU が MODE_SYNC を先に落とすため保留 collect items がパース終端
  flush で裸に出る→`screen_write_end_sync`（mode が立っているうちに
  discard）② `server_client_reset_state` が入力バッチ毎に pane カーソル
  /モードを naked 追従（カーソル散りの正体）→MODE_SYNC 中は skip
  ③ `screen_redraw_draw_pane` が deferred redraw 発火時に sync を強制
  解除し**半描画画面（2J 直後等）を atomic commit**→skip（ESU/1s timer
  が PANE_REDRAW 再点火）。検証: 合成 producer A/B で naked 72%→
  **0.26%**・半描画 commit 0・通常 pane/キー/窓操作 smoke 緑。
  **パッチ版を本番投入済（2026-06-11 深夜）**: keg
  `/opt/homebrew/Cellar/tmux/HEAD-86128a7/bin/tmux` を差し替え
  （原本=同 dir の `tmux.orig-86128a7`・3.6a keg も残置）→ 旧 server
  kill → monitor 自己治癒 14 窓再生成 → **実 claude 窓で naked
  13%→0.14% を実測**。⚠macOS の罠: 既存バイナリへの上書き cp は
  署名キャッシュ不整合で **exec が SIGKILL(exit137)**＝`rm`→`cp`
  （新 inode）→`codesign -s - -f` が必須（OS_REASON_CODESIGNING と
  同類）。⚠PR #5195 マージ前に `brew reinstall tmux --HEAD` を
  実行するとパッチが消える（マージ後に実行して正規化する）。

以下は cutover 時点の記録（背景として保持。「✅根本解決」判定は上記の
通り訂正）:

**結論(当時): tmux next-3.7 (master・issue 4744) が pane 側 DECSET 2026
を実装しており、これが探していた本質修正そのものだった。**

- **真因 (3 層全て実測で確定)**: ①proxy frame は元から完璧な atomic
  単位 (BSU+?25l+2J+全行+cursor 復元+?25h+ESU)。②tmux **3.6a は pane の
  DECSET 2026 を input.c で一切 parse せず捨てる**＝frame 境界を破壊して
  active pane の出力を 40-64% 裸 emit。③端末 (VSCode terminal は DECRQM
  実測 Ps=2 で認識・iTerm2 documented) は 2026 honor 済＝悪くなかった。
  **時間基準の外側 batch (tmux-wrap) では frame 途中に commit 境界が
  落ちて「半分描き直しの画面」が atomic に commit される＝原理的に解決
  不能**だった (user の「cls された状態で更新が走っていないか?」の指摘が
  正鵠)。
- **upstream 修正 (next-3.7・Chris Lloyd・issue 4744)**: pane BSU →
  MODE_SYNC on (1s safety timer)・**MODE_SYNC 中は outer への
  incremental 出力を discard**・pane ESU → PANE_REDRAW → sync wrap 済み
  全面 redraw 1 回＝**pane frame 1 つ = outer 1 commit** (frame 境界が
  end-to-end 保存)。
- **A/B 実測 (実 claude proxy frame・同一 workload)**: naked 40%→**2%**
  (残 2% は attach 初期化等の one-shot)・総 bytes 半減・実機目視で
  「完璧に動作」確認 (VSCode terminal / iTerm2)。
- **cutover 済 (2026-06-11)**: `brew unlink tmux && brew install tmux
  --HEAD` (next-3.7)。旧 3.6a server kill → monitor 自己治癒が 13 窓
  (dashboard+local6+↗remote6) を全自動再生成・cloud reconcile 無傷・
  全 18 package テスト next-3.7 で緑。**rollback**: 3.6a keg は
  `/opt/homebrew/Cellar/tmux/3.6a` に残置 (`brew unlink tmux && brew
  link tmux@3.6a 相当で復帰可`)。**3.7 正式リリース後に `brew upgrade
  tmux` で stable 復帰すること**。
- **fallback 残置 (旧 tmux 環境用)**:
  - `claude-master tmux-render -t <session>`: tmux -CC の %output
    (frame 無傷) を verbatim 転送する sync.js 相当の中間層。単一 pane
    viewer MVP。**3.6a 以前の tmux でも完璧描画**＝他 PC の tmux が
    古い時の即効薬。
  - `claude-master tmux-wrap -- tmux attach`: 時間基準 batch+sync-wrap。
    （⚠当時の「frame 整列不能」評価は 3.6a の出力前提。next-3.7 では
    frame ごと burst 化し整列する＝上記訂正節のとおり現役の緩和策）。
- **pomera firmware**: DECSET 2026 honor を実装すれば next-3.7 経由で
  同じ恩恵 (要 firmware 側対応)。

### 歴史的経緯（教訓・詳細は auto-memory 参照）

L4-A' 5-iter 失敗 (推測修正の連鎖) → 巻き戻し → measurement-first 転換
(3 経路生バイト capture / DECRQM probe / A/B 対照) で真因を 3 層に分解
→ -CC forwarder で「frame 境界保存が解」を実証 → upstream に同設計の
修正が既在と発見 → cutover。**「端末 capability・tmux 挙動は推測せず
probe/capture で機械判定」が最大の教訓** (`scripts/probe-term-sync.py`
/ `scripts/measure-render-path.sh` / `scripts/analyze-render-capture.py`)。

## 旧・対策案の記録（解決済みのため参考）

「どの PC からも tmux で同じ Claude Code」がプロジェクトの中核価値ゆえ、
tmux 経由の品質確保は重要。Web は borrowed PC / スマホの fallback 路。

- **案 A: `internal/ttysync` wrapper（idle-based byte batching）**:
  新 subcommand `claude-master tmux-wrap -- tmux ...` で tmux を子プロセス
  として PTY 経由 spawn、stdout を **idle 検出 (~4ms 無 byte で flush)**
  して 1 write に集約。2026 protocol 非依存＝端末が render-tick 基盤なら
  全端末で flicker 軽減期待。typing echo は idle 検出が短いため +2ms
  程度で人間知覚限界以下。**前提**: 「端末は受信 byte burst を render tick
  内に処理する」モデルが事実かを各端末で empirical 検証してから着手。
- **案 B: tmux upstream issue / patch**: 生バイト解析・minimum repro
  完備＝issue 品質は高い。merge は数ヶ月-年。自前 patch + Homebrew tap
  は配布 burden 大、Windows 側 psmux には届かない。**issue 投稿のみ
  並行・自前 patch は撤退デフォルト**。
- **案 C: pomera firmware を 2026 対応化**: user 制御下のクライアント
  なので確実に直せる唯一の path。pomera-tmux 専用 client 系で重要。
- **案 D: 端末選定 ガイド**: iTerm2/WezTerm/kitty/alacritty を tmux
  メイン端末として推奨、VSCode terminal/Terminal.app は案 A wrapper
  経由 or Web fallback を案内。

### 運用ガイド（端末別の推奨フロー）

| 用途 | 推奨端末 | tmux 経路 | flicker 状態 |
|---|---|---|---|
| 主作業 (VSCode 統合) | VSCode terminal (**2026 認識を DECRQM 実測済**) | **素の `tmux attach`** (tmux next-3.7 cutover 済) | ✅ 完璧 (実機確認済) |
| 主作業 (Mac native) | iTerm2 / WezTerm / kitty / alacritty | 素の `tmux attach` | ✅ 完璧 |
| 旧 tmux (≤3.6) しか無い PC | 任意の 2026 対応端末 | `claude-master tmux-render -t <session>` (-CC 中間層) | ✅ frame 無傷転送で完璧 (単一 pane MVP) |
| 主作業 (macOS 同梱) | Terminal.app | **使わない**（DECRQM NO-REPLY 実測・2026 非対応＝端末側が律速で全層健全でもちらつく） | ❌ (2026-06-12 実測) |
| pomera (改造クライアント) | 自前 firmware | 直接 | firmware 側で DECSET 2026 honor 実装すること |
| スマホ / 借りた PC | ブラウザ | Web (https 経由) | ✅ sync.js で完全 atomic |

`tmux-wrap` 使用例:
```bash
# 既存 tmux session に wrap 経由で attach (sync-wrap 既定 ON)
claude-master tmux-wrap -- tmux attach -t claude-master

# 短い idle (typing 即時優先) に上書き
claude-master tmux-wrap --idle-ms 2 -- tmux attach -t claude-master

# sync-wrap を切って素の idle batch のみ (比較検証用)
claude-master tmux-wrap --no-sync-wrap -- tmux attach -t claude-master
```

実装は `internal/ttysync/` + `cmd/claude-master/tmuxwrap.go`。tmux を
PTY 経由で子プロセス起動し、tmux→host stdout を **idle 検出 (~4ms 無
byte で flush)** で 1 write に集約し、**flush 全体を BSU/ESU で wrap
（sync-wrap・内側 marker は exact-match で除去）**＝m1 実測の「64%
裸 emit」を 0% にする。2026 認識端末（VSCode terminal 実測済・iTerm2
documented）で flush 単位の atomic 描画になる。加えて MaxHold (50ms)
が連続 stream でも描画を止めない backstop、MaxBuffer (512KB) がメモリ
保護。stdin→tmux は passthrough (typing latency +0)。
回帰検知: `internal/ttysync/{wrap,syncwrap}_test.go` で FakeClock 駆動
の burst 集約・分離・EOF flush・marker strip・split-marker carry・
MaxHold/MaxBuffer を機械検証。

端末対応の判定は推測禁止: `scripts/probe-term-sync.py`（DECRQM・目視
不要）で機械判定。DECRQM 未応答端末のみ `scripts/diag-terminal-render
.sh` の test4（目視）で最終判定。

検討から却下:
- ~~案 1 (proxy frame coalescing)~~: proxy frame は既に完璧。tmux 側で
  消費されるので effect 無し。
- ~~案 2 (tmux passthrough)~~: tmux 内側 VT 空で window 切替/resize で
  画面真っ白 race を構造的に潰せない。
- ~~案 3 (tmux 廃止)~~: 「どの PC からも tmux」中核価値破壊。
- ~~案 5 (Web 格上げ)~~: Web は fallback 路で primary に格上げは価値観違反。

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
