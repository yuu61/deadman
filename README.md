# deadman

deadman は ICMP echo によるホストの死活監視に特化した TUI ツールです。
イベントネットワークのような一時的なネットワーク構築に適しています。
元は Interop Tokyo ShowNet 向けの "pingman" であり、オリジナル [deadman](https://github.com/upa/deadman) の機能を引き継いでいます。

![demo](img/deadman-demo.gif)

## 特徴と機能

- **基本監視**: 1行1ホストの一覧をリアルタイム更新。IPv6対応、結果履歴、RTTバー、基本統計（LOSS / RTT / AVG / MIN / MAX / SNT）。
- **中継・プローブ**: ssh / snmp / netns / vrf / RouterOS API / tcp(hping3) / quic 経由の監視、送信元指定（`source=`）。
- **表示制御**: 段組み表示、表示精度の切り替え、統計列のトグル表示。ビューポートスクロール対応。
- **Go 版による強化**: クロスプラットフォーム単一バイナリ、特権の自動判別（外部 `ping` コマンド不要）、`via=quic` 監視、`nexthop` 強制プローブ、SIGHUP による設定リロード。

## インストール

[Releases](https://github.com/yuu61/deadman/releases) から OS・アーキテクチャに合ったバイナリをダウンロードして展開してください。

```sh
tar xzf deadman-v0.1.0-linux-amd64.tar.gz
cd deadman-v0.1.0-linux-amd64
./deadman deadman.conf
```

**ソースからビルドする場合:** (Go 1.25+)
```sh
git clone https://github.com/yuu61/deadman
cd deadman
go build -o bin/deadman ./cmd/deadman
```
または `go install github.com/yuu61/deadman/cmd/deadman@latest` を使用。

## 使い方

設定ファイルを指定して実行します。
```sh
./deadman deadman.conf  [options]
```

### オプション
- `-s, --scale N` : RTT バーグラフのスケール（ms 単位、既定 10）
- `-a, --async-mode` : 全対象へ並列に ping を送る
- `-b, --blink-arrow` : async モードで矢印を点滅させる
- `-l, --logging DIR` : DIR 配下に対象ごとのログを書き出す
- `-c, --split N` : 一覧を N 列の段組みで表示する（既定 1）

### 主なキー操作
- `↑` / `↓`: RTT バーのスケール変更
- `l`: RTT スケールの対数表示切替
- `p`: 統計値の表示精度切替（ms〜ms.3）
- `m` / `v` / `h` / `a`: 各列（MIN/MAX, VIA, HOSTNAME, ADDRESS）の表示切替
- `r`: 全対象の統計をリセット
- `R`: 設定ファイルの再読み込み (Windows。Unix は SIGHUP を使用)
- `[` / `]`: 段組みの列数を増減
- `j` / `k`, `PgDn` / `PgUp`, `g` / `G`: スクロール・ジャンプ操作
- `q` / `Ctrl-C`: 終了

## 設定ファイル

空白区切りで `名前 アドレス [属性...]` を 1 行に記述します。`---` のみの行でグループ化できます。
名前や属性にスペースを含む場合はダブルクォートで囲みます。

```text
google          173.194.117.176
googleDNS       8.8.8.8
---
kame            203.178.141.194
kame6           2001:200:dff:fff1:216:3eff:feb1:44d7

# 中継やプローブのオプション例
"Cloudflare QUIC" 1.1.1.1 via=quic
"SSH Relay"     192.168.1.1 relay=203.0.113.1 os=Linux user=admin
"NextHop"       10.0.0.1 nexthop=192.168.0.254 source=eth0
```

### 主な属性とディレクティブ
- `relay=...`, `via=...`: ssh や snmp、netns、quic などの中継モードを指定します。
- `source=...`: プローブの送信元（IPやインタフェース名）を指定。
- `resolve_family=ipv4|ipv6`: ホスト名の解決レコードを固定。
- ディレクティブ行: 設定の独立した行に `columns` (列表示), `scale` (RTTバー), `precision` (精度), `split` (段組み) を記述し、起動時の既定値を指定可能。

*(詳細な属性やディレクティブの仕様については [docs/configuration.md](docs/configuration.md) を参照してください)*

## プラットフォームに関する注意

- **Windows / macOS**: 特権不要で動作します。
- **Linux**: 直接 ICMP は raw ソケット（root / `CAP_NET_RAW`）を優先利用します。非 root 環境では非特権 ICMP（`SOCK_DGRAM`）を自動使用しますが、事前に `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` の設定が必要です。
- **表示フォント**: ブロック文字（`▁▂▃▄▅▆▇█`）が正しく表示されるUnicode対応端末（Windows Terminal等）を推奨します。Linux の fbcon 環境では `fbterm` と等幅フォント（Source Han Sans等）の利用を推奨します。

*(中継モードの実行要件や、Linuxコンソール環境における文字化け解消の詳細は [docs/platform_and_font.md](docs/platform_and_font.md) をご参照ください)*

## セキュリティに関する注意
設定ファイル（中継先ホストや宛先など）は外部コマンドの引数に渡される場合があります。インベントリなどから自動生成した信頼できない入力が設定ファイルに混入しないよう注意してください。

## ライセンス / 連絡先
- **License**: MIT
- **Author**: [Twitter@tukushityann](https://twitter.com/tukushityann) / <yuu@tukushityann.net>
