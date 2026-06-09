deadman
=======

deadman は ping を使ってホストの死活を監視する TUI ツールです。

deadman は多機能ではありません。ICMP echo によるホストの死活確認に特化しています。
カンファレンスやイベントネットワークのような一時的なネットワークの構築用途に適して
います。元々は Interop Tokyo ShowNet 向けに設計・実装されたツール（旧 "pingman"）です。

deadman は Go で実装されており、クロスプラットフォーム対応（Windows / Linux / macOS）と
単一バイナリ配布を特長とします。設定ファイル形式とコマンドラインフラグは、その源流である
pingman / オリジナル deadman から引き継いでいます。

源流となったオリジナルの実装は <https://github.com/upa/deadman> にあります。

![demo](/img/deadman-demo.gif)

ビルド
======

Go 1.26 以上が必要です。

 git clone <https://github.com/yuu61/deadman>
 cd deadman
 go build -o deadman ./cmd/deadman      # Windows: go build -o deadman.exe ./cmd/deadman

直接実行・インストールも可能です。

 go run ./cmd/deadman deadman.conf
 go install github.com/yuu61/deadman/cmd/deadman@latest

使い方
======

 ./deadman deadman.conf                 # Windows: deadman.exe deadman.conf

監視対象を変更するには、設定ファイルを編集または新規作成します。

$ cat deadman.conf
 google          173.194.117.176
 googleDNS       8.8.8.8
 ---

 kame            203.178.141.194
 kame6           2001:200:dff:fff1:216:3eff:feb1:44d7

設定ファイルの各行が 1 つの監視対象ホストを表します。ダッシュのみの行（`---`）は
区切り線として描画され、対象をグループ化するのに便利です。

中継方法やプロービングのオプションは、アドレスの後ろに `key=value` 形式の属性として
記述します。たとえば、リモートホスト経由（ssh）で ping を送る場合は次のようにします。

 google-via-ssh  173.194.117.176 relay=X.X.X.X os=Linux

これはリモートサーバ X.X.X.X 経由で google のサーバへ ping を送ります。ssh の
ユーザ名と鍵は `user=USER`、`key=KEYPATH` で指定できます。その他の中継モードは
`deadman.conf` 内にも記載があります。

| モード        | 記述例                                                                   |
|--------------|--------------------------------------------------------------------------|
| 直接 ICMP    | `googleDNS 8.8.8.8`                                                       |
| ssh 中継     | `name ADDR relay=SSHHOST os=Linux user=USER key=KEY`                     |
| snmp         | `name ADDR relay=SNMPHOST via=snmp community=COMMUNITY`                  |
| netns        | `name ADDR relay=NETNSNAME via=netns`（Linux・root）                      |
| vrf          | `name ADDR relay=VRFNAME via=vrf`（Linux・root）                          |
| routeros     | `name ADDR relay=ROS via=routeros_api username=U password=P method=https verify=false` |
| tcp/hping3   | `name ADDR tcp=dstport:80`（Linux・root）                                 |
| nexthop 強制 | `name ADDR nexthop=GWIP [source=eth0]`（直接 ICMP・Linux・root・IPv4）    |

任意の `source=...` 属性で、プローブの送信元を指定できます。指定できるのは IP アドレス
（全モード）か、もしくは直接 ICMP と Linux/macOS 上の ssh/netns/vrf 中継に限り、
`source=eth0` のようなネットワークインターフェース名です。

任意の `nexthop=GWIP` 属性で、直接 ICMP プローブを指定したゲートウェイ（next-hop）経由で
強制送出できます（特定経路の到達性を監視するのに有用）。AF_PACKET で L2 宛先をゲートウェイの
MAC に指定して送るため、**Linux + root/CAP_NET_RAW・IPv4 のみ**で動作します。ゲートウェイは
egress インタフェースの直結サブネット上（on-link）である必要があり、egress は `source=`
（インタフェース名または IP）で明示できます。relay/via/tcp を併用した場合や IPv6 ターゲットでは
nexthop は無視され、通常ルーティングで監視されます（その旨を起動時に警告します）。

> **注意（rp_filter）**: 強制した next-hop が通常経路と別インタフェースになる場合、Linux の
> reverse-path filter が strict（`net.ipv4.conf.*.rp_filter=1`）だと応答が破棄され、到達可能な
> ホストが `X`（ダウン）と表示されることがあります。その場合は `rp_filter` を 2（loose）または
> 0（off）にしてください。strict を検出すると deadman は起動時に警告を表示します。

オプション
==========

 -s, --scale N       RTT バーグラフのスケール（ms 単位、既定 10）
 -a, --async-mode    全対象へ並列に ping を送る
 -b, --blink-arrow   async モードで矢印を点滅させる
 -l, --logging DIR   DIR 配下に対象ごとのログファイルを書き出す

操作
====

 r           全対象の統計をリセットする（プログラムは動作したまま）
 R           設定ファイルを再読み込みする（Windows。Unix では SIGHUP を使用）
 q / Ctrl-C  終了する

Unix では deadman に SIGHUP を送ると設定ファイルを再読み込みします。このとき、既存の
エントリは履歴を保持します（名前・アドレスおよび中継属性で同一性を判定）。端末の
リサイズには自動で追随します。

権限とプラットフォームに関する注意
==================================

直接 ICMP はネイティブソケットを使用します（`ping` バイナリは不要）。

- **Windows**: 管理者権限への昇格なしで動作します。
- **Linux**: deadman は可能であれば raw ソケット（特権 ICMP）を自動的に使うため、
  **root もしくは `setcap cap_net_raw+ep ./deadman` を付与した実行ではそのまま動作します**。
  非 root かつ capability も無い場合は、非特権 ICMP（`SOCK_DGRAM`）を許可するために
  `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` を設定してください。
  なお `net.ipv4.ping_group_range` は呼び出し元の gid が範囲に含まれることを要求し、
  既定では root（gid 0）を含みません。非特権 LXC コンテナのように当該 sysctl を
  変更できない環境では、root か setcap（いずれも raw ソケット経路）を使ってください。
- **macOS**: そのまま動作します。

中継モードは外部コマンドを呼び出すため、それらが存在する環境でのみ動作します。
`ssh`（ssh 中継）、`snmpping`（snmp）、`ip`（netns/vrf）、`hping3`（tcp）が該当します。
netns・vrf・hping3 は Linux + root が前提です。RouterOS API モードは HTTP を使うため
OS 非依存です。必要なコマンドが存在しない環境（たとえば Windows）では、その対象は
クラッシュせず失敗（`X`）として表示されます。

`nexthop` 強制は AF_PACKET で L2 フレームを送るため Linux + root/CAP_NET_RAW が必須で、
他 OS のビルドではその対象は失敗（`X`）として表示されます。

ブロック文字による RTT バー（`▁▂▃▄▅▆▇█`）を正しく表示するため、Unicode と色に対応した
端末を推奨します（Windows では Windows Terminal）。

ライセンス
==========

MIT

連絡先
======

<yuu@tukushityann.net>
