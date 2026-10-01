# 動作環境と権限

## 直接 ICMP の権限

`probe=direct` の ICMP は、外部の `ping` コマンドを使わず、deadman 自身のソケットで送ります。

- **Windows / macOS**: 特権なしで動きます。
- **Linux**: raw ソケット（特権 ICMP）を開ければそれを使います。root で実行するか、実行ファイルに capability を付けてください。

  ```sh
  sudo setcap cap_net_raw+ep ./deadman
  ```

  root でも capability も無い場合は、非特権 ICMP（`SOCK_DGRAM`）を使います。これには、実行するユーザーのグループを `net.ipv4.ping_group_range` で許可しておく必要があります。WSL2 でもこの設定が必要です。

  ```sh
  sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
  ```

  非特権 LXC コンテナなど、sysctl を変更できない環境では、root か setcap を使ってください。

どちらのソケットも開けない場合は、起動時に画面上部へ警告と対処方法を表示し、直接 ICMP の行は `?` になります。

## 方式ごとの要件

| `probe=` | OS | 権限 | 外部コマンド |
| --- | --- | --- | --- |
| `direct` | すべて | [直接 ICMP の権限](#直接-icmp-の権限) | なし |
| `nexthop` | Linux のみ（ほかの OS では構築エラー） | root か `CAP_NET_RAW`（`ping_group_range` では足りない） | なし |
| `tcp` | Linux | root | `hping3` |
| `quic` | すべて | 不要 | なし |
| `ssh` | すべて | 不要 | `ssh`（中継先に `ping`） |
| `netns` / `vrf` | Linux | root | `ip`、`ping` |
| `routeros` | すべて | 不要 | なし |
| `snmp`（非推奨） | すべて | 不要 | なし |

- 外部コマンドが無い場合、その行は `?` になります。
- `nexthop` の行があり raw ソケットを開けない場合は、起動時に警告と対処方法を表示し、その行は `?` になります。
- ssh の中継先の OS（`os=`）ごとの違いは [ssh](configuration.md#ssh) を参照してください。

## プローブの待ち時間

1 回のプローブは、次の時間で打ち切ります。

| 方式 | 待ち時間 |
| --- | --- |
| `direct` / `nexthop` | 応答を 1 秒待つ |
| `ssh` / `netns` / `vrf` | 実行する ping が応答を 1 秒待ち、コマンド全体を 5 秒で打ち切る。ssh の接続待ちは 3 秒 |
| `ssh`（中継先が Darwin の IPv6） | 15 秒で打ち切る |
| `tcp` / `quic` / `routeros` / `snmp` | 5 秒 |

ssh の中継先が Darwin の IPv6 は `ping6` で送ります。macOS の `ping6` には応答を待つ時間を指定する方法が無く、応答が無いと約 10 秒待つため、その行のプローブは最大 15 秒かかります。
async モードでは、そのプローブが終わるまで次の巡回を始めません。

## 外部コマンドの後始末

外部コマンド（`ssh`・`ip`・`ping`・`hping3`）は、打ち切ったとき、再読み込みや終了で監視を止めたとき、コマンド自身が終わったときに、その子プロセスまで終わらせます。
Unix では専用のプロセスグループ、Windows では Job Object を使います。
ssh の ProxyCommand もこの範囲に含まれます。

## rp_filter

`nexthop` で強制したゲートウェイが、通常の経路と別のインタフェースにある場合の注意です（IPv4 のみ）。
Linux の reverse-path filter が strict（`net.ipv4.conf.*.rp_filter=1`）だと応答が破棄され、到達できるホストが到達できないように見えることがあります。
IPv4 の `nexthop` の行があり、strict なインタフェースがある場合は、起動時に警告します。
`rp_filter` を 2（loose）か 0（off）に設定してください。
