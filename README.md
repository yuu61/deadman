# deadman

deadman は ICMP echo によるホストの死活監視に特化した TUI ツールです。
イベントネットワークのような一時的なネットワーク構築に適しています。
元は Interop Tokyo ShowNet 向けの "pingman" であり、オリジナル [deadman](https://github.com/upa/deadman) の機能を引き継いでいます。

![demo](img/deadman-demo.gif)

## 特徴

- 1 行 1 ホストの一覧をリアルタイムに更新し、結果の履歴を RESULT バーに、統計（LOSS / RTT / AVG / MIN / MAX / JIT / SNT / FAIL）を列に表示します。IPv6 に対応しています。
- `resolve_family=ipv4|ipv6` で同じホスト名を IPv4 / IPv6 別に監視でき、ADDRESS 列の `[IPv4]` / `[IPv6]` で見分けられます。
- 監視元からの ICMP のほか、ゲートウェイを強制した ICMP（nexthop）、TCP 接続（IPv4 / IPv6、外部コマンド不要）、QUIC ハンドシェイク、ssh / netns / VRF / RouterOS API / SNMP（非推奨）を中継した ping で監視できます。
- Linux / macOS / Windows で動く単一バイナリです。ICMP は外部の `ping` コマンドを使わず、権限に応じてソケットを選びます。
- 段組み、列の表示、統計の精度、RESULT バーの文字と色を、キー操作と設定で切り替えられます。ブロック文字を表示できない端末では、自動で ASCII 表示になります。
- 設定は実行中に再読み込みでき、監視経路の同じ行は統計と履歴を引き継ぎます。`-l` で結果をファイルに記録できます。
- `--check` で監視を始めずに設定を検査し、`--format` でコメントを残して整形できます。

## インストール

[Releases](https://github.com/yuu61/deadman/releases) から OS・アーキテクチャに合ったアーカイブ（Windows は `.zip`、ほかは `.tar.gz`）をダウンロードして展開します。

```sh
tar xzf deadman-v0.1.0-linux-amd64.tar.gz
cd deadman-v0.1.0-linux-amd64
./deadman deadman.conf
```

ソースからビルドする場合は Go 1.26 以上が必要です。

```sh
git clone https://github.com/yuu61/deadman
cd deadman
go build -o bin/deadman ./cmd/deadman
```

または `go install github.com/yuu61/deadman/cmd/deadman@latest` を使います。

Windows / macOS は特権なしで動きます。
Linux で直接 ICMP を使うには、root で実行するか、次のどちらかを設定してください（詳細は [動作環境と権限](docs/platform.md)）。

```sh
sudo setcap cap_net_raw+ep ./deadman                          # raw ソケットを許可する
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"       # または非特権 ICMP を許可する
```

## 使い方

```sh
./deadman [options] deadman.conf
```

オプションは設定ファイルの前にも後にも書けます。

| オプション | 説明 |
| --- | --- |
| `-s`, `--scale N` | RESULT バーの 1 段あたりの幅（ms、既定 1、小数可） |
| `-a`, `--async-mode` | 全対象へ同時にプローブを送る（既定では 1 行ずつ順に送る） |
| `-b`, `--blink-arrow` | async モードで送信中の矢印を点滅させる |
| `-l`, `--logging DIR` | 結果を DIR 配下に行ごとのファイルで記録する（[ログ](docs/logging.md)） |
| `-c`, `--split N` | 一覧を N 列の段組みで表示する（既定 1） |
| `-g`, `--glyph MODE` | RESULT バーの文字。`auto`（既定）/ `block` / `ascii` / `digit` |
| `--check` | 設定の構文・属性・値を検査し、問題を行番号付きで報告して終了する |
| `--format` | 設定を検査し、整形した内容を標準出力へ出して終了する |

`-s`・`-c`・`-g` は、設定ファイルの同じ働きのディレクティブより優先されます。

### 設定のチェックと整形

```sh
./deadman --check deadman.conf
./deadman --format deadman.conf > deadman.formatted.conf
```

どちらも通信や TUI の起動を行わず、特権なしで使えます。
チェック成功時は `ファイル名: OK`、失敗時は標準エラーに `ファイル名:行番号: 理由` を出します。
終了コードは成功が `0`、設定や読み書きのエラーが `1`、オプションの誤りが `2` です。
`--check` と `--format` は同時に指定できません。

整形ではコメント・引用符内の内容・行順・空行を保ち、名前・アドレス・オプションの各列をスペースで左揃えにします。
列の開始位置はファイル全体の対象行で揃え、列間は最低 2 スペース、オプション同士は 1 スペースで区切ります。
元のファイルは書き換えません。保存するときは上の例のように別のファイルへ出力してください。
検査範囲と整形規則の詳細は [設定のチェックと整形](docs/configuration.md#チェックと整形) を参照してください。

### キー操作

| キー | 動作 |
| --- | --- |
| `q` / `Ctrl-C` | 終了する |
| `r` | 全対象の統計と履歴をリセットする |
| `R` | 設定ファイルを再読み込みする。Unix では SIGHUP でも再読み込みする（端末の切断による SIGHUP では終了する） |
| `↑` / `↓` | RESULT バーのスケールを変える |
| `l` | RESULT バーの対数表示を切り替える |
| `b` | RESULT バーの文字を切り替える（block → ascii → digit） |
| `p` | 統計の表示精度を切り替える（`ms` → `ms.1` → `ms.2` → `ms.3`） |
| `m` / `v` / `h` / `a` | MIN と MAX / VIA / HOSTNAME / ADDRESS 列の表示を切り替える |
| `[` / `]` | 段組みの列数を減らす / 増やす |
| `j` / `k`、`PgDn` / `PgUp`、`g` / `G`（`Home` / `End`） | 一覧をスクロールする |

### 画面の見方

RESULT バーは左が最新です。応答したプローブは RTT の段階に応じた高さと色で描きます。
`X` は応答の無かったプローブで、損失として数えます。
`t` / `s` / `?` は中継や監視元の失敗で対象を観測できなかったプローブで、死活が不明なので統計に加えません。
TCP の接続拒否も、対象からの応答を確認できないため `?` とし、統計に加えません（[TCP の判定](docs/configuration.md#tcp)）。
列・文字・色の詳細は [画面の見方](docs/display.md) を参照してください。

## 設定ファイル

1 行に `名前 アドレス [属性...]` を書きます。
`-` だけの語で始まる行は区切り線です。`--- Routers / Switches` のように後ろへ書いたコメントは、区切り線の中に表示します。
`#` で始まる行はコメントです。
名前などに空白を含めるときは二重引用符で囲みます。
IP アドレスの表記ゆれは吸収し、画面とログには短い標準表記を使います。
たとえば `008.008.008.008` は `8.8.8.8`、`2001:4860:4860:0:0:0:0:8888` は `2001:4860:4860::8888` になります。

```text
google            173.194.117.176
googleDNS         8.8.8.8
--- KAME
kame              203.178.141.194
kame6             2001:200:dff:fff1:216:3eff:feb1:44d7
---
"Cloudflare QUIC" 1.1.1.1      probe=quic
"SSH Relay"       192.168.1.1  probe=ssh relay=203.0.113.1 os=Linux user=admin
"NextHop"         10.0.0.1     probe=nexthop nexthop=192.168.0.254 source=eth0
```

監視方式は `probe=` で選びます（省略時は監視元からの ICMP）。
属性や値に誤りのある行は監視せず、RESULT 欄に理由を表示します。ほかの行は監視を続けます。
属性、監視方式、表示の既定値を決めるディレクティブ、旧版の書式からの移行は [設定ファイル](docs/configuration.md) を参照してください。

## ドキュメント

- [設定ファイル](docs/configuration.md): 書式、監視方式と属性、ディレクティブ、行の同一性、旧版からの移行
- [画面の見方](docs/display.md): 列、監視の進み方、RESULT バーの文字と色、行の強調、警告、コンソールフォント
- [動作環境と権限](docs/platform.md): ICMP の権限、方式ごとの要件、待ち時間、rp_filter
- [ログ](docs/logging.md): `-l` が書くファイルと行の形式

## セキュリティに関する注意

設定ファイルの値（中継先や宛先など）は、外部コマンドの引数に渡ることがあります。
インベントリなどから自動生成した、信頼できない入力が設定ファイルに混入しないよう注意してください。

## ライセンス / 連絡先

- **License**: MIT
- **Author**: [Twitter@tukushityann](https://twitter.com/tukushityann) / <yuu@tukushityann.net>
