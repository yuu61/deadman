# deadman

deadman は ping を使ってホストの死活を監視する TUI ツールです。

多機能ではなく、ICMP echo によるホストの死活確認に特化しています。
カンファレンスやイベントネットワークのような一時的なネットワークの構築用途に適しています。
元々は Interop Tokyo ShowNet 向けに設計・実装されたツール（旧 "pingman"）です。

Go で実装されており、クロスプラットフォーム対応（Windows / Linux / macOS）と単一バイナリ配布を特長とします。
設定ファイル形式とコマンドラインフラグは、その源流である pingman / オリジナル deadman（<https://github.com/upa/deadman>）から引き継いでいます。

![demo](img/deadman-demo.gif)

## ビルド

Go 1.26 以上が必要です。

```sh
git clone https://github.com/yuu61/deadman
cd deadman
go build -o deadman ./cmd/deadman      # Windows: go build -o deadman.exe ./cmd/deadman
```

直接実行・インストールも可能です。

```sh
go run ./cmd/deadman deadman.conf
go install github.com/yuu61/deadman/cmd/deadman@latest
```

## 使い方

```sh
./deadman deadman.conf                 # Windows: deadman.exe deadman.conf
```

監視対象は設定ファイルで指定します。
各行が 1 つの監視対象ホストを表し、ダッシュのみの行（`---`）は区切り線として描画され、対象をグループ化するのに便利です。

```console
$ cat deadman.conf
google          173.194.117.176
googleDNS       8.8.8.8
---

kame            203.178.141.194
kame6           2001:200:dff:fff1:216:3eff:feb1:44d7
```

設定ファイルの文法・属性は次の「[設定ファイル](#設定ファイル)」を参照してください。

## 設定ファイル

### 行の文法

各行は空白区切りで `名前 アドレス [key=value ...]` と解釈されます（最初のトークンが名前、2 番目がアドレス）。
名前にスペースを含めたい場合は**ダブルクォートで囲みます**。

```text
"Cloudflare via MGMT" 1.1.1.1 nexthop=10.98.38.9
```

クォートを付けないと `Cloudflare via MGMT 1.1.1.1 nexthop=...` は名前 `Cloudflare`・アドレス `via` と解釈され、`MGMT` と `1.1.1.1` は無視されてしまいます。
クォートは名前以外のトークンにも使え、スペースを含む属性値（例 `key="/path with space"`）も表現できます。
特別扱いされるのはダブルクォートだけで、シングルクォート `'` はリテラル文字です。
解釈できなかった余分なトークンがあると、deadman は起動時に警告を表示します。

### 中継モード

中継方法やプロービングのオプションは、アドレスの後ろに `key=value` 形式の属性として記述します。

| モード | 記述例 |
| --- | --- |
| 直接 ICMP | `googleDNS 8.8.8.8` |
| ssh 中継 | `name ADDR relay=SSHHOST os=Linux user=USER key=KEY` |
| snmp | `name ADDR relay=SNMPHOST via=snmp community=COMMUNITY` |
| netns | `name ADDR relay=NETNSNAME via=netns`（Linux・root） |
| vrf | `name ADDR relay=VRFNAME via=vrf`（Linux・root） |
| routeros | `name ADDR relay=ROS via=routeros_api username=U password=P method=https verify=false` |
| tcp/hping3 | `name ADDR tcp=dstport:80`（Linux・root） |
| nexthop 強制 | `name ADDR nexthop=GWIP [source=eth0]`（直接 ICMP・Linux・root・IPv4/IPv6） |

ssh 中継の例 `google-via-ssh 173.194.117.176 relay=X.X.X.X os=Linux` は、リモートサーバ X.X.X.X 経由で対象へ ping します。
`user=USER` / `key=KEYPATH` で ssh のユーザ名と鍵を指定でき、`os` を省略すると deadman を実行している OS 名が使われます。
各モードの記述例は `deadman.conf` にもコメントとして含まれています。
中継モードが必要とする外部コマンドや権限は「[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)」を参照してください。

### 属性

- **`source=...`** … プローブの送信元を指定します。
  指定できるのは IP アドレス（全モード）か、もしくは直接 ICMP と Linux/macOS 上の ssh/netns/vrf 中継に限り、`source=eth0` のようなネットワークインターフェース名です。

- **`resolve_family=ipv4|ipv6`** … ホスト名の解決をそのアドレスファミリーに固定します（`ipv4` は A レコード、`ipv6` は AAAA レコード）。
  dual-stack なホスト名を IPv4 と IPv6 で別々に監視したいときに使います。

  ```text
  web-v4 example.com resolve_family=ipv4
  web-v6 example.com resolve_family=ipv6
  ```

  - **直接 ICMP に限り**有効で、`relay`/`via`/`tcp`/`nexthop` を併用した対象では無視されます（それらは中継先やゲートウェイ側でファミリーが決まるため）。
  - 値は `ipv4`/`ipv6` のみを認識し、それ以外（綴り違いなど）は指定なし（自動）として扱います。
  - 指定したファミリーに該当レコードが無い場合や、反対のファミリーの IP アドレスリテラルを指定した場合は、その対象は `X`（到達不能）として表示されます。
  - 名前・アドレスが同じでも `resolve_family` が異なれば別エントリとして扱われ、リロード時の履歴も分かれて保持されます。
    VIA 列はどちらも `direct` になるため、上の例のように行名で区別すると見分けやすくなります。
  - ホスト名が同じファミリー内に複数のアドレス（複数の A / AAAA）を持つ場合、probe するのは **RFC 6724 で並べ替えた先頭の 1 アドレスだけ**です（アドレス選択は Go / OS のリゾルバに委譲し、複数アドレスを並行して試す Happy Eyeballs 的な動作は行いません。これは `resolve_family` を指定しない場合も同じです）。
    ラウンドロビン DNS では解決のたびに先頭が入れ替わりうるため、特定の 1 台を継続的に監視したい場合は IP アドレスを直接指定してください。

- **`nexthop=GWIP`** … 直接 ICMP プローブを指定したゲートウェイ（next-hop）経由で強制送出します（特定経路の到達性を監視するのに有用）。
  ゲートウェイは egress インタフェースの直結サブネット上（on-link）である必要があり、egress は `source=`（インタフェース名または IP）で明示できます。
  IPv6 のリンクローカルゲートウェイ（`fe80::/10`）はどのインタフェースでも on-link になり曖昧なため、`source=IFNAME`（インタフェース名）で egress を指定する必要があります（このとき送信元 IP は宛先のスコープに合わせて自動選択されます）。
  ゲートウェイとターゲットは同じアドレスファミリーである必要があり、relay/via/tcp を併用した場合は nexthop は無視されます（いずれも起動時に警告します）。
  プラットフォーム要件（Linux + root/CAP_NET_RAW）と rp_filter の注意は「[権限とプラットフォームに関する注意](#権限とプラットフォームに関する注意)」を参照してください。

統計列やスケール・精度・段組みの既定値を設定する `columns` / `scale` / `precision` / `split` ディレクティブについては、それぞれ「[列（カラム）の表示](#列カラムの表示)」「[表示精度（RTT バーと統計値）](#表示精度rtt-バーと統計値)」「[段組み（左右分割）表示](#段組み左右分割表示)」を参照してください。

## オプション

```text
-s, --scale N       RTT バーグラフのスケール（ms 単位、既定 10。実行中は ↑/↓ でも変更可）
-a, --async-mode    全対象へ並列に ping を送る
-b, --blink-arrow   async モードで矢印を点滅させる
-l, --logging DIR   DIR 配下に対象ごとのログファイルを書き出す
-c, --split N       一覧を N 列の段組みで表示する（既定 1。実行中は [ / ] でも変更可）
```

## 操作

```text
↑ / ↓       RTT バーのスケールを段階的に変える（1→2→5→10→20→50→100 ms。既存のバーも再描画）
p           統計値の表示精度を切り替える（ms → ms.1 → ms.2 → ms.3。常に ms 単位、小数桁数を増減）
r           全対象の統計をリセットする（プログラムは動作したまま）
R           設定ファイルを再読み込みする（Windows。Unix では SIGHUP を使用）
m           MIN / MAX 列の表示を切り替える
v           VIA 列（取得方法）の表示を切り替える
j / k       監視リストを 1 行スクロールする（下 / 上。行数が端末の高さを超える場合）
PgDn / PgUp 監視リストを 1 画面分スクロールする
g / G       監視リストの先頭 / 末尾へジャンプする
[ / ]       段組みの列数を増減する（] で増やし、[ で減らす。最小 1 列）
q / Ctrl-C  終了する
```

監視対象が端末の高さに収まらない場合、固定ヘッダ（タイトル・列見出し）はそのままに一覧部分だけがスクロールし、最下部に現在位置のインジケータ（例 `[1-40/120]`）を表示します。
`↑ / ↓` は RTT スケール用なので、スクロールには `j` / `k` を使います。
なお、テーブルは横方向にはスクロールしないため、列が収まる十分な端末幅を前提とします。

## 段組み（左右分割）表示

端末の縦幅が足りないが横幅に余裕がある場合、一覧を複数列に折り返して 1 画面に多くの対象を表示できます。
`]` で列数を増やし、`[` で減らします（最小 1 列）。起動時の既定列数は `-c` / `--split` フラグまたは設定ファイルの `split` ディレクティブで指定できます。

```text
split 2             一覧を 2 列で表示する（既定 1。明示した -c が split より優先）
```

一覧は**列メジャー**（縦→横）で流れます。すなわち左端の列を上から下まで埋め、続きを右隣の列に送ります（設定ファイルの順序を縦方向に保ちます）。
タイトル・キー凡例は全体で 1 つだけ共有し、列見出し（`HOSTNAME …`）は各列に付きます。複数インスタンスを並べる方式と違いヘッダが重複しないため、節約したい縦行を浪費しません。

列を増やすほど 1 列の幅が狭まり、deadman の看板である**結果バー（履歴グリフ）が短くなります**。
要求した列数が端末幅に収まらない場合は自動で列数を減らし、キー凡例に実効/要求列数（例 `([/])cols 2/3`）を表示します。
列をより多く詰めたいときは、`m` / `v` / `p` で統計列を隠すと 1 列あたりの幅が縮み、より多くの列が入ります。

設定ファイルの**再読み込み**（Unix では SIGHUP、Windows では `R` キー）では、既存のエントリは統計・履歴を引き継ぎます（名前・アドレスおよび中継属性で同一性を判定）。
`columns` の表示は設定ファイルの内容に戻りますが、`scale` と `precision` は**実行中の値が保持されます**（`-s` で起動した値やキーで調整した状態を、対象を入れ替えるだけの再読み込みで失わないため）。
端末のリサイズには自動で追随します。

## 列（カラム）の表示

統計列の表示は実行中にキーで切り替えられるほか、設定ファイルの `columns` ディレクティブで起動時の既定を指定できます。

```text
columns MIN=off MAX=off VIA=on
```

`KEY=on|off`（`true|false` / `yes|no` / `1|0` も可、大文字小文字を問わない）で各列の表示を指定します。
明示しなかった列は既定（表示）のままです。
指定できるキーは`LOSS RTT AVG MIN MAX JIT SNT FAIL VIA` です。

`VIA` 列は各対象の**取得方法**を表示します（`direct` / `nexthop GWIP` / `ssh HOST` / `snmp HOST` / `netns NAME` / `vrf NAME` / `routeros HOST` / `tcp PORT`）。
同じアドレスを別経路で監視している場合などに、一目で区別できます。
表示は設定上の意図ではなく**実際に使われる経路**を反映するため、relay/via/tcp が優先されて nexthop が無視される対象では、その実際のモード（`ssh` など）が表示されます。

## 表示精度（RTT バーと統計値）

RTT バーグラフの**スケール**と統計値の**表示精度**は、実行中にキーで切り替えられるほか、設定ファイルのディレクティブで起動時の既定を指定できます。

```text
scale 5             RTT バー 1 段あたりの ms（↑/↓ キーと同じ。既定 10）
precision ms.1      統計値の表示精度（ms / ms.1 / ms.2 / ms.3 のいずれか。既定 ms）
```

- `scale` … RTT バーのグリフ（`▁▂▃▄▅▆▇█`）1 段が表す ms 幅です。
  高速な LAN ではバーが潰れて差が見えないため、小さくすると見分けやすくなります。
  実行中は `↑`（粗く）/ `↓`（細かく）で `1→2→5→10→20→50→100 ms` の段階を移動でき、**画面上の既存のバーも即座に再描画されます**。
- `precision` … 統計列（RTT / AVG / MIN / MAX / JIT）の数値表記です。
  表示単位は常に ms で、`ms`（整数）/ `ms.1`（小数 1 桁）/ `ms.2`（小数 2 桁）/ `ms.3`（小数 3 桁 ＝ マイクロ秒相当）から選べます。
  `.N` の桁数を増やすほどサブミリ秒の差が見えますが列幅も広がります。
  実行中は `p` キーで循環します。

`scale` はコマンドラインの `-s` / `--scale` と同じ設定で、**明示した `-s` が `scale` ディレクティブより優先**されます（どちらも無ければ既定 10）。
`precision` にコマンドラインフラグはありません。
再読み込みをまたいだ値の保持については「[操作](#操作)」を参照してください。

## 権限とプラットフォームに関する注意

直接 ICMP はネイティブソケットを使用します（`ping` バイナリは不要）。

- **Windows**: 管理者権限への昇格なしで動作します。
- **Linux**: deadman は可能であれば raw ソケット（特権 ICMP）を自動的に使うため、**root もしくは `setcap cap_net_raw+ep ./deadman` を付与した実行ではそのまま動作します**。
  非 root かつ capability も無い場合は、非特権 ICMP（`SOCK_DGRAM`）を許可するために `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` を設定してください。
  なお `net.ipv4.ping_group_range` は呼び出し元の gid が範囲に含まれることを要求し、既定では root（gid 0）を含みません。
  非特権 LXC コンテナのように当該 sysctl を変更できない環境では、root か setcap（いずれも raw ソケット経路）を使ってください。
  WSL2 の既定値 `1 0` は start > end の**空レンジ**で誰も非特権 ICMP を使えないため、非 root で動かすには setcap か上記 sysctl のいずれかが必須です。
- **macOS**: そのまま動作します。

直接・`nexthop` 対象がありながら raw・非特権 ICMP のどちらのソケットも開けない場合、deadman は起動時に対処方法（setcap / sysctl）を案内する警告をヘッダ下に表示します。
この状態では全プローブが失敗（`X`）し、パケットは 1 つも送出されません。

中継モードは外部コマンドを呼び出すため、それらが存在する環境でのみ動作します。
`ssh`（ssh 中継）、`snmpping`（snmp）、`ip`（netns/vrf）、`hping3`（tcp）が該当します。
netns・vrf・hping3 は Linux + root が前提です。
RouterOS API モードは HTTP を使うためOS 非依存です。
必要なコマンドが存在しない環境（たとえば Windows）では、その対象はクラッシュせず失敗（`X`）として表示されます。

`nexthop` 強制は AF_PACKET で L2 フレームを送り、ゲートウェイの MAC を IPv4 は ARP（`/proc/net/arp`）、IPv6 は NDP（netlink）で解決するため Linux + root/CAP_NET_RAW が必須で、他 OS のビルドではその対象は失敗（`X`）として表示されます。

> **注意（rp_filter・IPv4 のみ）**: 強制した next-hop が通常経路と別インタフェースになる場合、Linux の reverse-path filter が strict（`net.ipv4.conf.*.rp_filter=1`）だと応答が破棄され、到達可能なホストが `X`（ダウン）と表示されることがあります。
> その場合は `rp_filter` を 2（loose）または 0（off）にしてください。
> strict を検出すると deadman は起動時に警告を表示します（IPv6 には同等の設定はありません）。

ブロック文字による RTT バー（`▁▂▃▄▅▆▇█`）を正しく表示するため、Unicode と色に対応した端末を推奨します（Windows では Windows Terminal）。

## ライセンス

MIT

## 連絡先

<yuu@tukushityann.net>
