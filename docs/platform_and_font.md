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

## RESULT バーの色

成功したプローブは、RESULT バーの段階に応じて色分けされます。段階は文字の高さや数字と同じもので、`scale`・対数表示・表示文字（`glyph`）によって決まります。最も速い段階が緑、中央の段階が黄、最も遅い段階（`█` / `@` / `9`）が赤で、その間は段階ごとに少しずつ変わります。`scale` を変えると色の境目も変わります。失敗（`X` / `t` / `s`）は、最も遅い段階の赤と区別するため紫（端末のマゼンタ）で表示します。

| 意味 | 色 | 値（24 ビット） |
| --- | --- | --- |
| 安全 | 緑 | `#03AF7A` |
| 注意 | 黄 | `#FFF100` |
| 危険 | 赤 | `#FF4B00` |
| 失敗（応答なし） | 紫 | 端末のマゼンタ（暗い背景では明るいマゼンタ） |

配色は次の考え方に基づいています。
- **色の意味**: 緑 = 安全、黄 = 注意、赤 = 危険という対応と、4 段階以上は赤と緑の間の色で表すという方針は、ISO 22324（色による警報の指針）に従っています。
- **失敗を紫にする理由**: 失敗まで赤だと「とても遅い」と見分けにくくなります。ISO 22324 は、赤より上の特別な危険に紫（または黒）を使うとしています。また紫は、1 型・2 型色覚でも青みが残り、黄土色寄りに見える遅い側の赤と区別できます。
- **色覚の多様性**: 3 色の値は、カラーユニバーサルデザイン推奨配色セット ver.4 のものです。この配色セットは JIS Z 9103:2018（安全色）の改正にも協力した CUDO によるもので、1 型・2 型色覚でも見分けやすいよう、緑は青寄り、赤は橙寄りになっています。
- **補間**: 中間の色は、知覚的に均等な色空間 Oklab で補間しています。sRGB のまま補間すると、緑と赤の間が濁った色になります。
- **色だけに頼らない**: 段階は文字の高さ（数字表示なら数字）でも読み取れます。

実際の色は端末に合わせて選ばれます。
- **24 ビットカラー**（`COLORTERM=truecolor` などを設定する端末）: 上記のグラデーションです。
- **256 色**（`TERM=xterm-256color` など）: 256 色パレット（16〜255 番）のうち最も近い色です。SSH で `COLORTERM` が転送されるのは、クライアントの `SendEnv` とサーバーの `AcceptEnv` の両方に含まれる場合だけです。転送されなければ、24 ビットカラー対応の端末でも SSH 越しでは 256 色で表示されます。
- **16 色**（Linux の仮想コンソール、`TERM=xterm` など）: 緑・黄・赤の 3 色で、色合いは端末の配色設定に従います。暗い背景では明るい緑・黄・赤を使います。Linux コンソールの通常の黄は茶色に、通常の赤は黒地で暗く見えるためです。
- **明るい背景**: 起動時に端末へ背景色を問い合わせます。背景が明るい場合は、白地に対するコントラスト比が 3:1（WCAG 2 の非テキストのコントラスト基準）以上になるよう暗くした色を使います。問い合わせに応答しない端末や tmux / screen の中では、暗い背景として扱います。
- **色なし**: 環境変数 `NO_COLOR` を設定すると、色を付けません（失敗の紫も含みます）。

## コンソールフォントと表示の崩れ

RTT バー（`▁▂▃▄▅▆▇█`）を正しく表示するため、Unicode に対応した端末（Windows Terminal 等）を推奨します。SSH 経由では接続元ターミナルのフォントが使われますが、Linux の仮想コンソール（tty等）に直接映像出力する場合、フォントの収録字数制限によってバーが欠けたり `#` のように崩れることがあります。

deadman は起動時にこれを自動判定し（`-g auto`、既定）、仮想コンソールのフォントにブロック文字が無い場合は RESULT バーを ASCII（`_.-=+*#@`、閾値はブロックと同じ）で表示します。数字表示（`-g digit` / `b` キー）も、ASCII だけで描けるので同様に使えます。判定の詳細や、判定できないケース（Linux コンソールから SSH で別ホストの deadman を開く場合など）は [configuration.md の `glyph`](configuration.md#result-バーの表示文字-glyph) を参照してください。

ブロック文字のまま表示したい場合は、以下のいずれかでフォントを差し替えてください（コンソールフォントを差し替えた場合は自動判定もブロック表示を選びます。fbterm は疑似端末なのでフォント判定の対象外となり、ブロック表示になります）。

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
