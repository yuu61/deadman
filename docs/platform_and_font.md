# 権限とプラットフォーム・フォントに関する注意

このドキュメントでは、`deadman` を実行する際のプラットフォームごとの権限要件や、コンソール環境でのフォント表示に関する詳細なトラブルシューティングを解説します。

## 権限と実行要件

直接 ICMP はネイティブソケットを使用します。

- **Windows / macOS**: 追加の設定なし（特権なし）でそのまま動作します。
- **Linux**:
  可能なら raw ソケット（特権 ICMP）を自動で使うため、**root もしくは `setcap cap_net_raw+ep ./deadman` を付与した実行**ではそのまま動作します。
  非 root かつ capability も無い場合は、非特権 ICMP（`SOCK_DGRAM`）を許可するため、以下の設定が必要です：
  ```sh
  sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
  ```
  非特権 LXC コンテナなどの sysctl を変更できない環境では、root か setcap を使用してください。WSL2 環境でも上記 sysctl の設定が必須です。

### 外部コマンドへの依存（中継モード）

中継モードは外部コマンドを呼び出すため、`ssh`, `snmpping`, `ip` (netns/vrf), `hping3` が存在する環境でのみ動作します。
- `nexthop` 強制や netns/vrf、tcp モードは Linux + root 権限が前提です。
- RouterOS API と quic モードは OS 非依存・外部コマンド不要で動作します。

> **注意（rp_filter・IPv4 のみ）**:
> 強制した next-hop が通常経路と別インタフェースになる場合、Linux の reverse-path filter が strict (`net.ipv4.conf.*.rp_filter=1`) だと応答が破棄され、到達可能なホストが到達不能として表示されることがあります。その場合は `rp_filter` を 2 (loose) または 0 (off) に設定してください。

## コンソールフォントと表示の崩れ

RTT バー（`▁▂▃▄▅▆▇█`）を正しく表示するため、Unicode に対応した端末（Windows Terminal 等）を推奨します。SSH 経由では接続元ターミナルのフォントが使われますが、Linux の仮想コンソール（tty等）に直接映像出力する場合、フォントの収録字数制限によってバーが欠けたり `#` のように崩れることがあります。

### 解決策1: fbterm + Source Han Sans (推奨)

`/dev/tty1` 等の HDMI 側の仮想コンソールで実行する場合、framebuffer 上で OTF を描画できる `fbterm` の利用が最も確実です。

```console
# Source Han Sans を導入
mkdir -p ~/.local/share/fonts/source-han-sans
curl -L -o /tmp/source-han-sans.zip \
  https://github.com/adobe-fonts/source-han-sans/releases/latest/download/01_SourceHanSans.ttc.zip
unzip -o /tmp/source-han-sans.zip -d ~/.local/share/fonts/source-han-sans
fc-cache -fv

# fbterm をインストールして起動（-s はフォントサイズ）
sudo apt install fbterm
fbterm -n "Source Han Sans" -s 32
./deadman deadman.conf
```
※ `/dev/fb0` にアクセスできるよう、一般ユーザーは `video` グループに追加してください (`sudo usermod -aG video "$USER"`)。

### 解決策2: コンソールフォントの差し替え (簡易版)

`fbterm` が使えない環境向けに、仮想コンソールのフォント自体を差し替える方法です。

```console
sudo apt install psf-unifont
sudo setfont /usr/share/consolefonts/Unifont-APL8x16.psf.gz
```

永続化するには `/etc/default/console-setup` に以下を設定し、`sudo setupcon --font-only` で反映します。
```ini
CHARMAP="UTF-8"
FONT="/usr/share/consolefonts/Unifont-APL8x16.psf.gz"
```
