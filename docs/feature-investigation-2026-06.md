# 機能調査レポート — 新機能追加・既存機能改善の機会

- **日付**: 2026-06-10
- **対象コミット**: `dedae0e`（master）
- **調査方法**: 6 次元の並列調査（プロービング層 / TUI / 設定・CLI / 統計・ログ / 参照ツール比較 / コード品質）→ 重複排除 → 各候補を保守者視点で敵対的検証。すべて実コード接地（`file:line`）で確認。
- **集計**: 候補 76 件 → ユニーク 52 件 → **採用 16 件 / 却下 35 件**。

## 評価軸（grain フィルタ）

README の哲学「deadman は多機能ではありません。ICMP echo によるホストの死活確認に特化」を、これまで実際に成長してきた具体軸に落とすと厳密に 2 つ。提案はこの軸に乗るかで一次フィルタする。

- **軸1 — プロービング/到達性の忠実度**: nexthop 強制, IPv6 NDP, RouterOS, SNMP, VRF, netns, tcp/hping3 の系譜。「実経路をどう叩くか」の精緻化。
- **軸2 — 単一ライブテーブルの密度/可読性**: precision トグル, scale ラダー, VIA 列, 列の表示制御の系譜。1 画面 1 テーブルを読みやすくする方向。

**軸外（実装可否と無関係に哲学却下）**: Prometheus/メトリクスパイプライン, webhook/アラート, traceroute/per-hop, マウス, テーマ, ダッシュボード/RRD, 常駐デーモン/systemd, マルチビュー, include/env 展開のような設定オーサリング利便, リモート syslog。

**契約**: 設定構文と CLI フラグは user-facing 契約（CLAUDE.md）。変更は additive（新しい任意属性/ディレクティブ/フラグを `relayKeys`/`directives`/`statColumns` の既存シームに 1 エントリ）を強く優先し、README を同時更新する。コード品質/堅牢性/テストの改善は軸フィルタの対象外で常に歓迎。

凡例: 〔軸 / additive|breaking / effort S(小)M(中)L(大) / 触れる不変条件〕

---

## 第1ティア — 強く推奨

軸ど真ん中・additive・効果/コスト比が高い。

### 1. ビューポート/スクロール 〔軸2 / additive / M / gen〕

`internal/tui/view.go:41` の `View()` は `for i, r := range m.rows` で**全行を無条件描画**しており、ビューポートが存在しない。`internal/tui/model.go:45` で `m.height` は取得済みなのに未使用（＝配線が途中で止まっている intent の痕跡）。ShowNet 規模（100+ ターゲット）という本来の用途で端末からあふれるのが最大の既存欠落。

**実装スケッチ**:

- `Model` に `scrollTop int` を追加。`handleViewKey`（`model.go:226`）の switch に `j/k/pgup/pgdn/g/G` を additive 追加し `scrollTop` を移動 + clamp。
- `handleReload` の `recalcWidths` 後に `scrollTop` を新 `len(m.rows)` でクランプ。
- `view.go` の描画ループを可視窓に限定。**重要**: `arrowFor` が `m.arrowIdx`/`m.inflight` を**絶対行インデックス**前提で参照するため、`for i := top; i < bottom; i++ { r := m.rows[i]; … targetLine(i, …) }` と絶対 index を保ち、range の再ベース化を避ける。
- 可視行数 = `m.height` − 固定ヘッダ/フッタ行数 − `len(m.warnings)`。bubbles 依存は不要（手書きスライスで足り、ミニマリズムにも合致）。

### 2. per-target の `timeout=` 属性 〔軸1 / additive / S / なし(+Key)〕

現状は全モードがグローバル定数 timeout 固定（ICMP `icmp.go:13` 1s、subprocess/snmp/hping/routeros 5s）。WAN(>1s) と LAN(<10ms) 混在網で過剰/不足が実害になる。

**実装スケッチ**:

- `internal/config/config.go` の `relayKeys`（95-100 行）に `"timeout": true` を 1 エントリ。
- `internal/ping/ping.go` の `Spec` に `Timeout time.Duration`（`"3s"` を `ParseDuration`）。各 `Send` 冒頭の `WithTimeout` 引数を `if s.Timeout>0 { s.Timeout } else { 既存const }` に。icmp は `pinger.Timeout` も同様に。
- `internal/tui/model.go:105` の Spec 構築で `Timeout` をコピー。
- `internal/monitor/target.go` の `Key()` に source/tcp と同様 `:timeout=` を追記（履歴保持のため）。
- README に新属性を追記（契約変更）。

**注意**: 同時に上がった `count=` は**分離して却下**した。`Pinger.Send` は単一 Result を返す契約で、`Consume`（`target.go:83-103`）が 1 結果＝1 Snt＝1 history グリフを前提とする。`count>1` は Send シグネチャ変更（不変条件違反）か `parse.go` 改修を要する breaking。timeout 単体に絞れば S。

### 3. 行フィルタ（Down のみ表示） 〔軸2 / additive / S / なし〕

列可視性制御（`columns` ディレクティブ + `v`/`m` キー、`columns.go` の visible マップ）の**行版**。100+ ホスト時の認識負荷を下げる。#1（ビューポート）と組むと効果が大きい。

**実装スケッチ**:

- `Model` に `filterDown bool`。`handleViewKey`（`model.go:226`）に `case "f": m.filterDown = !m.filterDown; return m`（列幅は変わらず `recalcWidths` 不要）。
- `View()`（`view.go:41`）のループ先頭に `if m.filterDown && r.Target != nil && r.Target.State == monitor.Up { continue }`。
- ヘルプ行（`view.go:26`）に `(f)ilter` を追記、README キー一覧にも追記。
- probe/gen/Consume には一切触れない（render-only）。最小の「Down のみトグル」に厳格に留める（複合条件 UI・検索ボックスに広げると軸外に滑る）。

### 4. ホスト名の AF 固定 `resolve_family=ipv4|ipv6` 〔軸1 / additive / S / なし〕

`internal/ping/icmp.go:67` は pro-bing の `NewPinger(addr)` を使い `network="ip"` 既定のため、ホスト名を v4/v6 限定に固定する利用者向け手段がない。dual-stack 名を A/AAAA 別行で監視したい実需に応え、IPv6 NDP・nexthop の既存投資と一貫する。

**実装スケッチ**:

- `config.go:95-100` の `relayKeys` に `"resolve_family": true` を追加（`Spec.Relay` に格納）。
- `icmp.go`: `icmpPinger` に `network string` フィールドを追加し、`newICMPPinger` で `s.Relay["resolve_family"]` を `"ip4"`/`"ip6"` に正規化（不明値は `""` で従来どおり自動）。`Send` 内を `pinger := probing.New(p.addr); if p.network != "" { pinger.SetNetwork(p.network) }; if err := pinger.Resolve(); err != nil { return Result{Code: Failed, TTL: -1} }` に差し替え（`NewPinger` は即時 Resolve するため `New` に分解して `SetNetwork` を挟む）。
- `Key()` は無改修（relay マップ経由で同名 2 行が別 Key になり履歴保持が成立）。
- README の属性表に追記し、MethodDirect 限定・relay/via/tcp 併用時は無視の旨を nexthop と同様に明記。

### 5. TCP/hping3 の flags・source-port 拡張 〔軸1 / additive / S / なし〕

`internal/ping/hping.go:46` は `-S -c 1` 固定で、確立済みセッション検証や source-port FW ルール検証ができない。tcp/hping3 自体が軸1の成長分であり、既存 sanctioned プローバの忠実度拡張。

**実装スケッチ**:

- `hping.go` 内で完結。`hpingPinger` に `sport`/`flags`/`window` フィールドを追加し、`newHPingPinger` で `opts["sport"]/["flags"]/["window"]` を取り込む。
- `Send` の引数を動的構築: flags 文字（S/A/F/R/P/U）を hping3 オプション（-S/-A/-F/-R/-P/-U）へ写すマップ、flags 未指定時は現行 `-S` にフォールバック（後方互換）。`sport:N`→`-s N -k`（`-k` でポート固定）、`window:N`→`-w N`。`-c 1`・`-p port` は据え置き。
- README の `tcp=` 構文に新キーを追記。

---

## 第2ティア — 状況により有用

| # | 提案 | 軸 | 種別 | add/break | effort | 要点 |
| --- | --- | --- | --- | --- | --- | --- |
| 6 | **DSCP/ToS マーキング** (`dscp=EF`) | 軸1 | new | additive | M | QoS 経路検証（`ping -Q`/`fping -O` 相当）。直接ICMP は `SetTrafficClass`、nexthop は手組ヘッダの ToS。`EF/AF41/CS5/46` を解決する小テーブル（`precisionModes` 同様の単一ソース） |
| 7 | **ソート** (loss/state/name 順) | 軸2 | new | additive | M | `displayOrder []int` 間接層 + `slices.SortStableFunc`。Sep をアンカーに各セグメント内のみ並べ替え。probe 側（`commands.go`）は config 順固定で矢印ズレ回避。**live-rtt は除外**（毎秒ジャンプして追跡を壊す） |
| 8 | **Down 継続時間列 `SINCE`** | 軸2 | new | additive | M | `Target.LastStateChange` を遷移時のみ記録 → `DOWN 3m` 表示。`statColumns` に固定列 1 エントリ。NOC トリアージ用 |
| 9 | **直近 N 窓の loss 率列** | 軸2 | new | additive | M | lifetime `LossRate`（`target.go:97`）は長時間運用で老化。`WindowLossRate(n)` + 既定 OFF 列。任意で `window` ディレクティブ（`scaleDirective` 同型）。RESULT バーの定量化 |
| 10 | **`--check`/`--validate` フラグ** | 軸CLI | new | additive | S | パースして警告だけ出して終了（CI/投入前の typo・dropped token 検出）。`startupWarnings`（`parse_warn.go`）を export ラップして `main()` から呼ぶ。決定的な parse 警告が主目的（nexthop 系は環境依存） |
| 11 | **TTL 列** | 軸2 | new | additive | S | `Target.TTL`（`target.go:57,90`）は捕捉済み・未表示。`statColumns` に固定列 1 エントリ。**observ 可は直接 ICMP/RouterOS のみ**で他モードは `-1` → 空白/`-` にマップする専用 Cell が必須。最も安いが採用群で価値は最低 |

---

## 第3ティア — ポリッシュ / 堅牢化（「既存機能改善」側）

| # | 提案 | 軸 | 種別 | effort | 要点 |
| --- | --- | --- | --- | --- | --- |
| 12 | **async ラウンド中リロードの inflight/pending 一貫性テスト** | code-quality | test | S | `handlePingResult` の gen ガード（`model.go:343`）が `pending--`（`model.go:363`）より前にある順序を回帰固定。リファクタで順序が崩れると新ラウンドの pending が早期に `<=0` になり `scheduleNextRound` 二重発火するが、現状それを捕まえるテストが無い |
| 13 | **RTT≥10000ms の列あふれ処理** | 軸2 | bugfix | S | `columns.go:26-28` が桁あふれを明示認識済みだが未処理。各 `precisionMode.Format`/`cell()` で `mode.Width` クランプ。relay 系で稀に発生（直接 ICMP は 1s timeout で到達不可） |
| 14 | **SNMP v3 認証/暗号化対応** | 軸1 | improve | M | `snmp_version`/`snmp_user`/`snmp_auth`/`snmp_priv` 属性。ニッチの中のニッチ + テスト基盤新設が要るため優先度低 |
| 15 | **未知 `columns` キーの警告 + サジェスト** | 軸CLI | improve | M | `applyColumn`（`config.go:233`）/`buildVisible`（`columns.go:157`）が registry 外キーを黙って捨てる。`columnWarnings` + 距離 ≤2 の Levenshtein 近傍提示。relay 属性 typo は既に Dropped 警告で表面化済みのため scope 外 |
| 16 | **historyCap 境界動作テスト** | code-quality | test | S | `historyCap=256`（`target.go:35`）の超過 drop と truncate 後 newest-first 順を未検証（既存テストは最大 3 エントリ）。将来の ring buffer 化の安全網 |

---

## 検討したが却下（哲学フィルタが働いた証跡 / 35 件の代表）

- **軸外（9 件）**: Prometheus エクスポート, webhook/アラート, traceroute, マウス, テーマ, 常駐デーモン/systemd, リモート syslog, RRD/ダッシュボード, マルチビュー — README の「特化」哲学に正面から反する。
- **設定オーサリング利便（軸外）**: `include`/`import`, 環境変数展開, `defaults` ブロック, `--version` — ping 忠実度もテーブル密度も上げない。
- **冗長（既に別機構が担う）**:
  - 状態変化ハイライト → RESULT バーが newest-first（`target.go:99`）で既に遷移の新しさを符号化（最左が最新グリフ）。
  - ヘルプ画面（`?`）→ footer（`view.go:26`）が全キーを列挙済みで省略キー無し。
  - freeze → 行は位置固定でスクロールバックも無く、混乱の前提が成立しない。
  - Stddev 列 → JIT（RFC3550 EWMA）+ MIN/MAX が既にばらつきを可視化。
  - 詳細展開（Enter）→ 情報の大半が既にテーブル + 結果バー上に存在。
- **breaking / 低価値**:
  - per-target `interval` → グローバルラウンドクロックの構造改修（pending/inflight/ディスパッチ作り直し）で L+。
  - `count` → 単一 Result／単一 history グリフ契約と衝突。
  - パケットサイズ/DF/payload → 8 モード中 4（SNMP/RouterOS/SSH/TCP）で黙殺＝契約破綻。
  - DNS 再解決制御/アドレス変動可視化 → IP リテラル運用が主で実需が薄く、一部は `Key()` 誤認（`Key()` は解決後 IP でなく設定文字列 `t.Addr` を使う）に基づく。
  - ログのリモート出力/ローテーション/バッファリング/ring buffer 化 → コスト微小 or invariant-bearing パスへの負債で逆効果リスク。
  - ログのパストラバーサル対策 → `t.Name` は信頼済み config 由来（意図的 `#nosec G304`）。

---

## まとめ・推奨

このプロジェクトは「軸1 の深掘り」と「軸2 の窓化」にまだ伸び代がある。一押しは:

- **#1 ビューポート + #3 行フィルタ** — 大規模監視の実用性を一段上げる組。
- **#2 timeout + #4 resolve_family + #5 TCP flags** — いずれも effort S で軸1 を素直に伸ばす。

最小で型を見るなら **#3 行フィルタ** か **#2 timeout** が着手しやすい。すべての変更で `make check` を通し、契約（設定構文/CLI）に触れるものは README を同時更新すること。
