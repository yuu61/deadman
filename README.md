# deadman

deadman は ICMP echo によるホストの死活監視に特化した TUI ツールです（Go 製・Windows / Linux / macOS 向けの単一バイナリ）。多機能を狙わず、カンファレンスやイベントネットワークのような一時的なネットワーク構築に適しています。元は Interop Tokyo ShowNet 向けの "pingman"。

設定ファイル形式と CLI フラグは、源流の pingman / オリジナル deadman（<https://github.com/upa/deadman>）から引き継いでいます。

![demo](img/deadman-demo.gif)

## 機能

源流の deadman / pingman 由来の機能と Go 版での追加機能。詳細は各リンク先を参照してください。

### 基本機能（源流の deadman / pingman 由来）

- ICMP echo によるホスト死活監視 — 1 行 1 ホストの一覧をリアルタイムに更新します。IPv6 対応、状態に応じた色分け、結果履歴グリフ（成功バー / `X` 到達不能 / `t` ssh タイムアウト / `s` ssh エラー）。
- ssh / snmp / netns / vrf / RouterOS API / tcp(hping3) 経由のプローブと送信元指定（`source=`）。（[中継モード](#中継モード)）
- RTT バーと基本統計 — unicode ブロック（`▁▂▃▄▅▆▇█`）による RTT バーとスケール調整（`-s`）、LOSS / RTT / AVG / SNT 列。
- 結果のファイルロギング — 対象ごとに RTT・統計をログファイルへ書き出します（`-l`）。
- 設定ファイルとグループ化 — 空白区切りの `名前 アドレス key=value` 文法、`#` コメント、ダッシュのみの行（`---`）による区切り線でのグループ化。（[設定ファイル](#設定ファイル)）
- 統計リセットと設定リロード — `r` で全統計をリセット、SIGHUP で設定を再読み込みし、一致する対象の統計・履歴を引き継ぎます。（[操作](#操作)）

### Go 版 deadman で追加した機能

- **Go 単一バイナリ・クロスプラットフォーム — Python 版からの全面書き換え**
- ネイティブ ICMP ソケットと特権の自動判別 — 外部 `ping` コマンド不要。raw ソケット（root / `CAP_NET_RAW`）と非特権 `SOCK_DGRAM` を環境に応じて自動選択し、権限不足は起動時に診断警告します。（[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)）
- `via=quic`（QUIC ハンドシェイク到達性監視） — TLS1.3 ハンドシェイクの確立までを RTT として測ります。raw 権限不要・OS 非依存。（[中継モード](#中継モード)）
- `nexthop` 強制プローブ（IPv4/IPv6） — 直接 ICMP を指定ゲートウェイ（next-hop）経由で強制送出し、特定経路の到達性を監視します（Linux / root）。（[属性](#属性)）
- `resolve_family=ipv4|ipv6` — ホスト名解決を A / AAAA レコードに固定し、dual-stack なホストを IPv4 / IPv6 で別々に監視します。（[属性](#属性)）
- 列の表示制御 — VIA / MIN / MAX / JIT / FAIL / HOSTNAME / ADDRESS 列のトグル（`m` / `v` / `h` / `a`）と `columns` ディレクティブ。VIA 列は実際に使われた取得経路を表示します。（[列（カラム）の表示](#列カラムの表示)）
- 表示精度とスケール拡張 — 統計値の小数桁の切り替え（`p` / `precision`）、小数スケール、対数（log）スケール（`l`）。（[表示精度（RTT バーと統計値）](#表示精度rtt-バーと統計値)）
- 段組み（左右分割）表示 — 一覧を複数列に折り返し、1 画面により多くの対象を表示します（`-c` / `split` / `[` `]`）。（[段組み（左右分割）表示](#段組み左右分割表示)）
- ビューポート / スクロール — 端末の高さを超える一覧を、固定ヘッダはそのままにスクロールします（`j` / `k`・`PgUp` / `PgDn`・`g` / `G`、現在位置インジケータ付き）。（[操作](#操作)）
- カーネルタイムスタンプによる RTT 計測（Linux） — 受信時刻に `SO_TIMESTAMPNS` を用い、ループバックや静かな対象でも計測精度を高めます。
- 引数インジェクション対策 — `-` で始まる設定値を拒否し、外部コマンドへの引数注入を防ぎます。（[セキュリティに関する注意](#セキュリティに関する注意)）

## 目次

- [deadman](#deadman)
  - [機能](#機能)
    - [基本機能（源流の deadman / pingman 由来）](#基本機能源流の-deadman--pingman-由来)
    - [Go 版 deadman で追加した機能](#go-版-deadman-で追加した機能)
  - [目次](#目次)
  - [ビルド](#ビルド)
  - [インストール（リリース版バイナリ）](#インストールリリース版バイナリ)
  - [使い方](#使い方)
  - [設定ファイル](#設定ファイル)
    - [行の文法](#行の文法)
    - [中継モード](#中継モード)
    - [属性](#属性)
  - [オプション](#オプション)
  - [操作](#操作)
  - [段組み（左右分割）表示](#段組み左右分割表示)
  - [列（カラム）の表示](#列カラムの表示)
  - [表示精度（RTT バーと統計値）](#表示精度rtt-バーと統計値)
  - [権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)
  - [セキュリティに関する注意](#セキュリティに関する注意)
  - [ライセンス](#ライセンス)
  - [連絡先](#連絡先)

## ビルド

Go 1.25 以上が必要です（配布用バイナリは標準ライブラリのセキュリティ修正を取り込むため最新パッチ版 Go を推奨）。

```sh
git clone https://github.com/yuu61/deadman
cd deadman
go build -o bin/deadman ./cmd/deadman      # Windows: go build -o bin/deadman.exe ./cmd/deadman
# or
make build
```

直接実行・インストールも可能です。

```sh
go run ./cmd/deadman deadman.conf
go install github.com/yuu61/deadman/cmd/deadman@latest
```

## インストール（リリース版バイナリ）

[Releases](https://github.com/yuu61/deadman/releases) から OS / アーキテクチャに合ったアーカイブ（linux / macOS は `.tar.gz`、Windows は `.zip`、ファイル名 `deadman-<version>-<os>-<arch>`）をダウンロードして展開します。展開すると同名ディレクトリに実行ファイル・設定例 `deadman.conf`・README・LICENSE が入っています（実行ファイル名は常に `deadman` / `deadman.exe`）。

```sh
tar xzf deadman-v0.1.0-linux-amd64.tar.gz   # Windows: deadman-v0.1.0-windows-amd64.zip を展開
cd deadman-v0.1.0-linux-amd64
./deadman deadman.conf                       # Windows: .\deadman.exe deadman.conf
```

同梱の `deadman.conf` は監視対象の**例**です。実際の監視先に書き換えてください（既存ファイルの上書きに注意）。文法は「[設定ファイル](#設定ファイル)」を参照してください。

完全性は Releases 併載の `SHA256SUMS` で検証できます。

```sh
# ダウンロードしたアーカイブと SHA256SUMS を同じディレクトリに置いて実行
sha256sum --ignore-missing -c SHA256SUMS
# macOS: shasum -a 256 -c SHA256SUMS  (全アーカイブを置くか、対象 1 件を手動照合)
```

Linux で raw ソケット権限が必要な場合は「[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)」を参照してください。

> **メンテナ向け**: 配布アーカイブは `make package` で `dist/` に生成されます（`.tar.gz` / `.zip` と `SHA256SUMS`、要 `zip` / GNU `tar`）。`git tag -a vX.Y.Z` を付けて `git push origin vX.Y.Z` すると `release` ワークフローが全プラットフォーム分をビルドして GitHub Release へ公開します。

## 使い方

```sh
./bin/deadman deadman.conf                 # Windows: ./bin/deadman.exe deadman.conf
```

監視対象は設定ファイルで指定します。各行が 1 つの監視対象ホスト、ダッシュのみの行（`---`）はグループ化の区切り線です。

```console
$ cat deadman.conf
google          173.194.117.176
googleDNS       8.8.8.8
---

kame            203.178.141.194
kame6           2001:200:dff:fff1:216:3eff:feb1:44d7
```

設定ファイルの文法・属性は「[設定ファイル](#設定ファイル)」を参照してください。

## 設定ファイル

### 行の文法

各行は空白区切りで `名前 アドレス [key=value ...]` と解釈されます。名前にスペースを含めるなら**ダブルクォートで囲みます**。

```text
"Cloudflare via MGMT" 1.1.1.1 nexthop=10.98.38.9
```

クォートしないと `Cloudflare via MGMT 1.1.1.1 nexthop=...` は名前 `Cloudflare`・アドレス `via` と解釈され、残りは無視されます。クォートは名前以外のトークンにも使え、スペースを含む属性値（例 `key="/path with space"`）も表せます。特別扱いされるのはダブルクォートだけで、シングルクォート `'` はリテラル文字です。解釈できない余分なトークンがあると起動時に警告します。

先頭トークンが `columns` / `scale` / `precision` / `split`（大文字小文字を問わない）の行は、監視対象ではなく**設定ディレクティブ**として解釈されます（予約語のため同名ホストは記述できません）。`#` で始まる行（先頭インデント可）は行コメント、行中の `;#` 以降は行末コメントとして無視されます（`;#` を値に含めるならダブルクォートで囲みます）。

### 中継モード

中継方法やプロービングのオプションは、アドレスの後ろに `key=value` 属性として記述します。

| モード | 記述例 |
| --- | --- |
| 直接 ICMP | `googleDNS 8.8.8.8` |
| ssh 中継 | `name ADDR relay=SSHHOST os=Linux user=USER key=KEY` |
| snmp | `name ADDR relay=SNMPHOST via=snmp community=COMMUNITY` |
| netns | `name ADDR relay=NETNSNAME via=netns`（Linux・root） |
| vrf | `name ADDR relay=VRFNAME via=vrf`（Linux・root） |
| routeros | `name ADDR relay=ROS via=routeros_api username=U password=P method=https verify=false` |
| quic | `name ADDR via=quic [port=443] [sni=NAME] [verify=on]`（OS 非依存・外部コマンド不要） |
| tcp/hping3 | `name ADDR tcp=dstport:80`（Linux・root） |
| nexthop 強制 | `name ADDR nexthop=GWIP [source=eth0]`（直接 ICMP・Linux・root・IPv4/IPv6） |

ssh 中継（例 `google-via-ssh 173.194.117.176 relay=X.X.X.X os=Linux`）は `relay=` のサーバ経由で対象へ ping します。`user=USER` / `key=KEYPATH` で ssh のユーザ名と鍵を指定でき、`os` を省略すると deadman を実行している OS 名が使われます。`os=` は中継先（Unix 系）の OS を表し、リモートで使う `ping`/`ping6` の選択や送信元フラグ（`-I`/`-S`）に影響します。Windows の中継先（`ping -n`）は未対応です。

`quic` モード（`via=quic`）は対象に QUIC（TLS1.3）ハンドシェイクを毎回張り、その**確立完了までの時間**を RTT として測ります（既定ポート 443・ALPN `h3`）。ICMP の「任意 IP の死活」ではなく QUIC/h3 ポートの到達性を測る `tcp=` 接続プローブの仲間で、RTT は両端の TLS1.3 暗号処理を含むぶん ICMP よりやや高めに出ます。`port=` / `alpn=` / `sni=` / `verify=` で挙動を調整できます（「[属性](#属性)」参照）。外部コマンド不要・OS 非依存・raw 権限不要です。

各モードの記述例は `deadman.conf` にもコメントとして含まれます。必要な外部コマンドや権限は「[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)」を参照してください。

### 属性

- **`source=...`** … プローブの送信元（送信元 IP アドレス、または `source=eth0` のようなインターフェース名）を指定します。**直接 ICMP・`nexthop`・ssh/netns/vrf 中継**で有効です。
  インターフェース名を使えるのは直接 ICMP・`nexthop`（Linux）と **Linux 上**の ssh/netns/vrf 中継のみ。macOS/BSD の中継先では `ping -S` が送信元**アドレス**しか受け付けないため IP アドレスを指定してください（インターフェース名は構築時に拒否され起動時に警告）。
  snmp / routeros / tcp(hping3) / quic は `source` を使いません（中継先で生成・hping3 未対応・QUIC は `[::]:0` を bind のいずれか）。指定しても無視し起動時に警告します（監視は継続）。

- **`verify=on|off`**（routeros / quic モード）… TLS 証明書検証の有無を指定します。綴りは両モード共通で `on` / `true` / `yes` / `1` が検証あり、`off` / `false` / `no` / `0` が検証なし（大文字小文字を問わない）。ただし**既定値はモードで逆**です。
  - routeros … 既定**有効**（安全側）。自己署名証明書の機器向けに `off` 等で無効化でき、認識できない値は検証有効として扱います。
  - quic … 既定**無効**（`InsecureSkipVerify`）。IP アドレスを直接対象にすることが多く証明書/SAN 検査がほぼ毎回失敗するためです。`on` 等で有効化でき、認識できない値は無効のままです。

- **`port=` / `alpn=` / `sni=`**（quic モード）… ダイヤル先ポート（既定 `443`）、ALPN（既定 `h3`）、TLS の SNI（`ServerName`）を指定します。`sni` を省略するとアドレス欄のホスト名（IP アドレスリテラルの場合は空）が使われます。検証無効でも SNI は送出されるため、実在の h3 フロント（Cloudflare / Google など）を IP アドレスで対象にするときは `sni=` の指定が必要になることがあります。

- **`resolve_family=ipv4|ipv6`** … ホスト名の解決を A（`ipv4`）/ AAAA（`ipv6`）レコードに固定し、dual-stack なホスト名を IPv4 / IPv6 で別々に監視します。

  ```text
  web-v4 example.com resolve_family=ipv4
  web-v6 example.com resolve_family=ipv6
  ```

  - **直接 ICMP と `via=quic` でのみ**有効です（どちらも対象名をローカルで解決するため）。その他のモード（`relay`/`via=snmp/netns/vrf/routeros_api`/`tcp(hping3)`/`nexthop`）併用時は、ファミリーが中継先・ゲートウェイ側や各モードのリゾルバ任せになるため無視されます。
  - 認識するのは `ipv4`/`ipv6` のみ。それ以外（綴り違いなど）は指定なし（自動）扱いです。
  - 指定ファミリーに該当レコードが無いホスト名は `X`（到達不能）。直接 ICMP では反対ファミリーの IP アドレスリテラルも `X` ですが、`via=quic` では IP リテラルはファミリー確定済みのため対象外で、そのままダイヤルします。
  - 名前・アドレスが同じでも `resolve_family` が異なれば別エントリとして扱われ、リロード時の履歴も分かれて保持されます。VIA 列は同じ表示になるため、上の例のように行名で区別すると見分けやすくなります。
  - ホスト名が同一ファミリー内に複数アドレス（複数の A / AAAA）を持つ場合、probe するのは **RFC 6724 で並べ替えた先頭の 1 アドレスだけ**です（アドレス選択は Go / OS のリゾルバに委譲し、Happy Eyeballs 的な並行試行は行いません）。ラウンドロビン DNS では解決のたびに先頭が入れ替わりうるため、特定の 1 台を継続監視したい場合は IP アドレスを直接指定してください。

- **直接 ICMP はユニキャストの単一ホスト向け**です。ブロードキャスト / マルチキャスト宛への直接 ICMP は対象外で、`X`（到達不能）として表示されます。

- **`nexthop=GWIP`** … 直接 ICMP プローブを指定ゲートウェイ（next-hop）経由で強制送出し、特定経路の到達性を監視します。
  ゲートウェイは egress インタフェースの直結サブネット上（on-link）である必要があり、egress は `source=`（インタフェース名 / IP）で明示できます。IPv6 リンクローカルゲートウェイ（`fe80::/10`）はどのインタフェースでも on-link で曖昧なため、`source=IFNAME`（インタフェース名）が必須です（送信元 IP は宛先スコープに合わせ自動選択）。
  ゲートウェイとターゲットは同一アドレスファミリーである必要があります。relay/via/tcp 併用時は nexthop は無視され、いずれも起動時に警告します。プラットフォーム要件（Linux + root/CAP_NET_RAW）と rp_filter の注意は「[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)」を参照してください。

統計列やスケール・精度・段組みの既定値を設定する `columns` / `scale` / `precision` / `split` ディレクティブは、それぞれ「[列（カラム）の表示](#列カラムの表示)」「[表示精度（RTT バーと統計値）](#表示精度rtt-バーと統計値)」「[段組み（左右分割）表示](#段組み左右分割表示)」を参照してください。

## オプション

```text
-s, --scale N       RTT バーグラフのスケール（ms 単位、既定 10、小数可。実行中は ↑/↓ でも変更可）
-a, --async-mode    全対象へ並列に ping を送る
-b, --blink-arrow   async モードで矢印を点滅させる
-l, --logging DIR   DIR 配下に対象ごとのログファイルを書き出す
-c, --split N       一覧を N 列の段組みで表示する（既定 1。実行中は [ / ] でも変更可）
```

設定ファイルは 1 つだけ指定できます（フラグの前後どちらでも可）。複数指定した場合はエラーになります。

## 操作

```text
↑ / ↓       RTT バーのスケールを段階的に変える（既存のバーも再描画）
l           RTT スケールの対数表示を切り替える（linear → 底 e → 底 e²）
p           統計値の表示精度を切り替える（ms → ms.1 → ms.2 → ms.3）
r           全対象の統計をリセットする（プログラムは動作したまま）
R           設定ファイルを再読み込みする（Windows。Unix では SIGHUP を使用）
m           MIN / MAX 列の表示を切り替える
v           VIA 列（取得方法）の表示を切り替える
h           HOSTNAME 列の表示を切り替える
a           ADDRESS 列の表示を切り替える
j / k       監視リストを 1 行スクロールする（下 / 上。行数が端末の高さを超える場合）
PgDn / PgUp 監視リストを 1 画面分スクロールする
g / G       監視リストの先頭 / 末尾へジャンプする
[ / ]       段組みの列数を増減する（] で増やし、[ で減らす。最小 1 列）
q / Ctrl-C  終了する
```

監視対象が端末の高さに収まらない場合、固定ヘッダ（タイトル・列見出し）はそのままに一覧部分だけがスクロールし、最下部に現在位置のインジケータ（例 `[1-40/120]`）を表示します。テーブルは横方向にはスクロールしないため、列が収まる十分な端末幅を前提とします。

## 段組み（左右分割）表示

端末の縦幅が足りないが横幅に余裕がある場合、一覧を複数列に折り返して 1 画面に多くの対象を表示できます。実行中は `]` / `[` で列数を増減でき（最小 1 列）、起動時の既定は `-c` / `--split` フラグまたは `split` ディレクティブで指定します。

```text
split 2             一覧を 2 列で表示する（既定 1。明示した -c が split より優先）
```

一覧は**列メジャー**で流れ（左端の列を上から下まで埋めてから右隣へ送る）、設定ファイルの順序が縦方向に保たれます。タイトル・キー凡例は全体で 1 つだけ共有し、列見出し（`HOSTNAME …`）は各列に付くため、複数インスタンスを並べる方式と違ってヘッダが重複せず縦行を無駄にしません。

列を増やすほど 1 列の幅が狭まり、**結果バー（履歴グリフ）が短くなります**。要求した列数が端末幅に収まらない場合は自動で減らし、キー凡例に実効/要求列数（例 `([/])cols 2/3`）を表示します。`m` / `v` / `p` で統計列を隠すと 1 列あたりの幅が縮み、より多くの列を詰められます。

設定ファイルの**再読み込み**（Unix では SIGHUP、Windows では `R` キー）では、既存のエントリは統計・履歴を引き継ぎます（名前・アドレスおよび中継属性で同一性を判定）。`columns` の表示は設定ファイルの内容に戻りますが、`scale`（log モードの状態を含む）・`precision`・`split`（段組み列数）は**実行中の値が保持されます**（`-s`/`-c` やキー操作で調整した状態を、対象を入れ替えるだけの再読み込みで失わないため）。端末のリサイズには自動で追随します。

## 列（カラム）の表示

`RESULT`（結果バー）以外のすべての列は表示を切り替えられます。実行中のキー（`m` で MIN / MAX、`v` で VIA、`h` で HOSTNAME、`a` で ADDRESS）のほか、`columns` ディレクティブで起動時の既定を指定できます。

```text
columns ADDRESS=off MIN=off MAX=off VIA=on
```

`KEY=on|off`（`true|false` / `yes|no` / `1|0` も可、大文字小文字を問わない）で指定し、明示しなかった列は既定（表示）のままです。指定できるキーは `HOSTNAME ADDRESS VIA LOSS RTT AVG MIN MAX JIT SNT FAIL`。`RESULT`（結果バー）だけは常に表示され、隠せません。

`VIA` 列は**実際に使われた取得方法**を表示します（`direct` / `nexthop GWIP` / `ssh HOST` / `snmp HOST` / `netns NAME` / `vrf NAME` / `routeros HOST` / `tcp dstport:PORT` / `quic PORT`）。同じアドレスを別経路で監視している場合などに一目で区別でき、relay/via/tcp が優先されて nexthop が無視される対象では、その優先された経路（`ssh` など）が表示されます。

## 表示精度（RTT バーと統計値）

RTT バーグラフの**スケール**と統計値の**表示精度**は、実行中のキーのほか設定ディレクティブで起動時の既定を指定できます。

```text
scale 5             RTT バー 1 段あたりの ms（既定 10、小数可 例 scale 0.5）
precision ms.1      統計値の表示精度（ms / ms.1 / ms.2 / ms.3 のいずれか。既定 ms）
```

- `scale` … RTT バーのグリフ（`▁▂▃▄▅▆▇█`）1 段が表す ms 幅です。高速な LAN ではバーが潰れて差が見えないため、小さくすると見分けやすくなります。実行中は `↑`（粗く）/ `↓`（細かく）で `0.01〜100 ms`（サブミリ秒を含む）の段階を移動でき、**画面上の既存のバーも即座に再描画されます**。
- `log` モード … `l` キーで RTT スケールの**対数表示**を切り替えます（linear → 底 e → 底 e²）。桁をまたぐ広いレンジを 1 本のバーに収めたいときに使います（高速 LAN の低 RTT が最下段に潰れるのを防げます）。対数モードでは `↑`/`↓` で設定するスケール値が**対数窓の左端（floor）**として働き、係数（底）で窓の広さが決まります（底 e で 7 段が約 3.0 桁、底 e² で約 6.1 桁）。ヘッダ表示は `RTT floor 10ms xe` のように floor と係数の表記に変わります（係数ラベルは底 e が `xe`、底 e² が `xe2`）。起動時は linear（オフ）で、CLI / 設定ファイルからの初期指定はありません。
- `precision` … 統計列（RTT / AVG / MIN / MAX / JIT）の数値表記です。単位は常に ms で、`ms`（整数）/ `ms.1`（小数 1 桁）/ `ms.2`（小数 2 桁）/ `ms.3`（小数 3 桁 ＝ マイクロ秒相当）から選べます。桁を増やすほどサブミリ秒の差が見えますが列幅も広がります。実行中は `p` キーで循環します。

`scale` はコマンドラインの `-s` / `--scale` と同じ設定で、**明示した `-s` が `scale` ディレクティブより優先**されます（どちらも無ければ既定 10）。実用範囲（0.0001〜1000000 ms）を外れた極端な値・不正値（0・負数・`inf`・`nan`）は無視され、もう一方の有効な値（不正な `-s` に対しては `scale` ディレクティブ）、それも無ければ既定 10 が使われます。無視された `-s` は起動時に警告します。`precision` にコマンドラインフラグはありません。再読み込みをまたいだ値の保持は「[段組み（左右分割）表示](#段組み左右分割表示)」を参照してください。

## 権限とプラットフォームに関する注意

直接 ICMP はネイティブソケットを使用します（`ping` バイナリは不要）。

- **Windows**: 管理者権限への昇格なしで動作します。
- **Linux**: 可能なら raw ソケット（特権 ICMP）を自動で使うため、**root もしくは `setcap cap_net_raw+ep ./deadman` を付与した実行ではそのまま動作します**。非 root かつ capability も無い場合は、非特権 ICMP（`SOCK_DGRAM`）を許可する `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` を設定してください。`ping_group_range` は呼び出し元の gid が範囲に含まれることを要求し、既定では root（gid 0）を含みません。非特権 LXC コンテナのように当該 sysctl を変更できない環境では、root か setcap（いずれも raw ソケット経路）を使ってください。WSL2 の既定値 `1 0` は start > end の**空レンジ**で誰も非特権 ICMP を使えないため、非 root で動かすには setcap か上記 sysctl が必須です。
- **macOS**: そのまま動作します。

直接・`nexthop` 対象がありながら raw・非特権 ICMP のどちらのソケットも開けない場合、deadman は起動時に対処方法（setcap / sysctl）を案内する警告をヘッダ下に表示します。この状態では 1 つもパケットを送れず、全プローブが失敗（`X`）します。

中継モードは外部コマンドを呼び出すため、`ssh`（ssh 中継）、`snmpping`（snmp）、`ip`（netns/vrf）、`hping3`（tcp）が存在する環境でのみ動作します。netns・vrf・hping3 は Linux + root 前提。RouterOS API モードは HTTP を使うため OS 非依存。`quic` モードは in-process（quic-go による UDP ダイヤル）で外部コマンド不要・OS 非依存・raw ソケット権限（root / `CAP_NET_RAW`）不要です。必要なコマンドが存在しない環境（たとえば Windows）では、その対象はクラッシュせず失敗（`X`）として表示されます。

`nexthop` 強制は AF_PACKET で L2 フレームを送り、ゲートウェイの MAC を IPv4 は ARP（`/proc/net/arp`）、IPv6 は NDP（netlink）で解決するため Linux + root/CAP_NET_RAW が必須です。他 OS のビルドではその対象は失敗（`X`）として表示されます。

> **注意（rp_filter・IPv4 のみ）**: 強制した next-hop が通常経路と別インタフェースになる場合、Linux の reverse-path filter が strict（`net.ipv4.conf.*.rp_filter=1`）だと応答が破棄され、到達可能なホストが `X`（ダウン）と表示されることがあります。
> その場合は `rp_filter` を 2（loose）または 0（off）にしてください。
> strict を検出すると deadman は起動時に警告を表示します（IPv6 には同等の設定はありません）。

ブロック文字による RTT バー（`▁▂▃▄▅▆▇█`）を正しく表示するため、Unicode と色に対応した端末を推奨します（Windows では Windows Terminal）。SSH 経由では接続元ターミナルのフォントが使われますが、本体の映像出力で Linux の仮想コンソール（`tty1` など）に表示する場合は本体側のコンソールフォントが使われます。SSH では問題なく見えても本体側でバーが `#` や空白のように崩れる場合はフォントを設定してください。

本体直結画面の Linux 仮想コンソール（fbcon）は PSF 形式のビットマップフォントしか読めず、収録字数の制限で `▁▂▃▄▅▆▇█` が欠けたり潰れたりします。確実に表示するには framebuffer 端末 `fbterm` で OTF を描画するのが簡単です。

**推奨: fbterm + Source Han Sans**

`fbterm` は framebuffer 上で fontconfig + FreeType により OTF を描画します。`/dev/tty1` など HDMI 側の仮想コンソールで実行してください（SSH 上では効果なし）。`/dev/fb0` が必要で、開けない一般ユーザーは `video` グループに入れます（`sudo usermod -aG video "$USER"`、再ログインで反映）。

```console
# Source Han Sans を導入（https://github.com/adobe-fonts/source-han-sans/releases/latest）
mkdir -p ~/.local/share/fonts/source-han-sans
curl -L -o /tmp/source-han-sans.zip \
  https://github.com/adobe-fonts/source-han-sans/releases/latest/download/01_SourceHanSans.ttc.zip
unzip -o /tmp/source-han-sans.zip -d ~/.local/share/fonts/source-han-sans
fc-cache -fv

# fbterm を起動し、その中で deadman を動かす（-s はフォントサイズ、解像度に合わせて調整）
sudo apt install fbterm
fbterm -n "Source Han Sans" -s 32
printf '▁▂▃▄▅▆▇█ X\n'   # fbterm 内で表示確認
./deadman deadman.conf
```

> `fbterm` は native framebuffer / VESA ドライバ前提で `vga16fb` では動作しません。`/dev/fb0` が無い環境では使えないため、その場合は下記のコンソールフォント差し替えに切り替えてください。

**簡易な代替: コンソールフォントの差し替え**

端末を増やさず、仮想コンソールのフォント自体を差し替える方法です。Debian / Ubuntu / Proxmox VE 系では `psf-unifont` で RTT バーを表示できます（fbterm より字形は粗い）。

```console
sudo apt install psf-unifont
sudo setfont /usr/share/consolefonts/Unifont-APL8x16.psf.gz   # 現在のコンソールへ即時反映
printf '▁▂▃▄▅▆▇█ X\n'
```

永続化するには `/etc/default/console-setup` に設定して `sudo setupcon --font-only` で反映します。

```ini
CHARMAP="UTF-8"
FONT="/usr/share/consolefonts/Unifont-APL8x16.psf.gz"
```

## セキュリティに関する注意

設定ファイルは**コードと同等に信頼できる必要があります**。中継モードはしばしば root（または `CAP_NET_RAW`）で動くため、設定ファイルに書き込める者は deadman の権限で外部コマンドを起動できます。
`relay` / アドレスなどの値はそのまま `ssh` / `ping` / `ip` / `snmpping` / `hping3` の引数に渡されます。インベントリや CMDB から自動生成した値など、信頼できない入力を設定へ流し込まないでください。
オペランド位置に来る値（ssh 中継ホスト、netns/vrf 名、ping 先アドレス）が `-` で始まる場合は拒否され、その対象は永続的に失敗（`X`）として表示されます。

## ライセンス

MIT

## 連絡先

[Twitter@tukushityann](https://twitter.com/tukushityann)

<yuu@tukushityann.net>
