# CLAUDE.md

deadman は ping によるホスト死活監視 TUI（Go 製）。設定構文と CLI は旧 pingman / オリジナル deadman（<https://github.com/upa/deadman>）を源流に持つが、現在は deadman 自身の契約として管理する。
ツールの使い方・設定構文・CLI フラグ・プラットフォーム別の権限要件は README.md を参照（ここでは重複させない）。

このファイルは、コードを編集する際に知っておくべき不変条件と作業コマンドだけを記す。

## コマンド

作業の検証はすべて Makefile 経由で行う（`make help` で一覧）。

| 目的 | コマンド |
| --- | --- |
| ビルド | `make build` （`bin/deadman` を出力） |
| テスト | `make test` （`-count=1`、キャッシュ無効） |
| レース検出 | `make test-race` |
| 実 ICMP を伴う手動テスト | `make test-manual` （`//go:build manual`、実際にパケットを送る） |
| リント | `make lint` / 自動修正 `make lint-fix` |
| 整形 | `make fmt`（gci/gofumpt/golines） |
| **完了前ゲート** | **`make check`** = `tidy-check fmt-check vet lint test` |

コミット・PR を出す前、および「完了」と判断する前に必ず `make check` を通すこと。

## 編集時の原則：設定構文と CLI は利用者向けの契約

設定ファイルの文法と CLI フラグは deadman の **user-facing なインタフェース**であり、
利用者の設定ファイルやスクリプトが依存する契約である。源流（pingman / オリジナル
deadman）から引き継いだものだが、もはや Python 版とのバイト互換に縛られはしない。
ただし変更は破壊的になりうるため、**意図的に行い、README を必ず同時に更新する**こと。
一方、パース・フラグ・結果コードの**内部表現**は Go のイディオムに従ってよい
（外部から観測できる挙動を変えない限り）。

## アーキテクチャの不変条件

これらは個々のファイルを読んだだけでは見えにくく、知らずに触ると壊れる。

- **Bubble Tea / Elm アーキテクチャ（`internal/tui`）**: `Update` は単一ゴルーチンで実行されるため、ターゲット統計（`monitor.Target`）に**ロックは不要**。
  状態変更は必ず  `Update`（メッセージ駆動）経由で行い、別ゴルーチンから直接書き換えない。
  プローブは  command（ゴルーチン）として走り、結果を `pingResultMsg` として返す。
- **世代カウンタ（`gen`）**: 設定リロード（SIGHUP / `R` キー）でターゲット集合が入れ替わる。
  リロードで `gen` がインクリメントされ、古い世代のタイマー・在飛行中プローブのメッセージは破棄される（無効な行インデックスでの適用を防ぐ）。
  プローブ用メッセージを追加するときは必ず `gen` を持ち回り、`Update` 側で現行世代と照合すること。バイパスしない。
- **失敗は `error` でなく `ping.Result.Code` で返す**（`Pinger.Send` は error を返さない）。
  中継コマンド不在（例: Windows に `ssh` が無い）などの失敗も `X`/`t`/`s` グリフとしてグレースフルに表現される。この設計を崩さない。
- **リロード時の履歴保持**: ターゲットの同一性は `monitor.Target.Key()`（name + addr + ソート済み relay 属性）で判定し、一致するエントリは統計・履歴を引き継ぐ。

## パッケージ構成

- `cmd/deadman` — エントリポイント。`parseArgs` はフラグと位置引数の混在を許す
  （`flag` を繰り返し呼んで実現する）。
- `internal/config` — 設定ファイルのパース（`TargetSpec`）。文法は README に記載。
- `internal/ping` — 単一プローブの抽象化。`Pinger` インタフェースと各モード実装
  （直接 ICMP / SSH / SNMP / netns / vrf / RouterOS REST / tcp(hping3)）。`ping.New` が
  `via` 属性などからモードを選択してディスパッチする。
- `internal/monitor` — ping 層と TUI の間の per-target 状態・統計・結果バーのグリフ化。
- `internal/tui` — Bubble Tea のモデル / 更新 / 描画。

## プラットフォーム分岐

ビルドタグで OS 差を吸収している。`reload_unix.go`（`//go:build unix`、SIGHUP 配線）と
`reload_other.go`（`//go:build !unix`、no-op）のように、対になるファイルの両方を更新する。
全プラットフォーム向けのクロスビルドは `make cross`。
