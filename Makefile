# deadman の開発用 Makefile。
# `make` または `make help` で利用可能なターゲット一覧を表示する。

# ---- 設定 ------------------------------------------------------------------
GO            ?= go
GOLANGCI      ?= golangci-lint
# golangci-lint を `make tools` で入れる際のバージョン (現行 v2 系)。
GOLANGCI_VERSION ?= v2.12.2

# アーカイブ生成に使う外部ツール (macOS で GNU tar を使う場合は `make package TAR=gtar`)。
# zip の変数は ZIP_BIN とする: Info-ZIP の zip は環境変数 $ZIP / $ZIPOPT を既定オプション
# として読むため、make 変数を ZIP と名付けて export すると引数解釈が壊れる (出力が作られない)。
TAR           ?= tar
ZIP_BIN       ?= zip

BIN_NAME  := deadman
PKG       := ./cmd/deadman
BIN_DIR   := bin
DIST_DIR  := dist

# 実行ファイルの拡張子 (Windows は .exe、その他は空)。go env GOEXE は GOOS の上書きにも追従する。
EXE       := $(shell $(GO) env GOEXE)

# git から導出するバージョン文字列 (タグが無ければコミットハッシュ、未コミット差分は -dirty)。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# アーカイブを決定的にするためコミット日時を SOURCE_DATE_EPOCH に固定する
# (tar の mtime 正規化・gzip -n と併用)。環境変数が設定されていればそちらを優先。
SOURCE_DATE_EPOCH := $(or $(SOURCE_DATE_EPOCH),$(shell git log -1 --pretty=%ct 2>/dev/null || echo 0))

# zip は SOURCE_DATE_EPOCH を解さないため、固める前に touch で各ファイルの mtime を固定する。
# touch -d @epoch は GNU 専用なので、GNU/BSD 双方が解せる touch -t 形式 (ホスト TZ) へ変換する。
# date が epoch を扱えない環境でも決定性を失わないよう、最終手段として DOS 下限の 1980-01-01 に倒す。
SOURCE_DATE_TOUCH := $(or $(shell date -d @$(SOURCE_DATE_EPOCH) +%Y%m%d%H%M.%S 2>/dev/null || date -r $(SOURCE_DATE_EPOCH) +%Y%m%d%H%M.%S 2>/dev/null),198001010000.00)

# リリースビルド用フラグ。-s -w でシンボルを除去、-trimpath で絶対パスを排除して再現性を上げる。
# -X main.version で git 由来のバージョン文字列をバイナリへ埋め込み、TUI のタイトル表示に反映する。
LDFLAGS   := -s -w -X main.version=$(VERSION)
BUILDFLAGS := -trimpath -ldflags '$(LDFLAGS)'

# クロスコンパイル対象 (GOOS/GOARCH)。
PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64 windows/arm64

.DEFAULT_GOAL := help

# ---- ビルド ----------------------------------------------------------------
.PHONY: build
build: ## 現在の OS/アーキテクチャ向けにビルド (bin/deadman)
	$(GO) build $(BUILDFLAGS) -o $(BIN_DIR)/$(BIN_NAME)$(EXE) $(PKG)

.PHONY: cross
cross: ## 全プラットフォーム向けにクロスコンパイル (dist/ へ出力)
	@mkdir -p $(DIST_DIR)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		out=$(DIST_DIR)/$(BIN_NAME)-$(VERSION)-$$os-$$arch$$ext; \
		echo ">> $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			$(GO) build $(BUILDFLAGS) -o $$out $(PKG) || exit 1; \
	done

.PHONY: package
package: ## リリース用アーカイブ(.tar.gz/.zip)と SHA256SUMS を dist/ に生成 (要 GNU tar/gzip/zip/sha256sum)
	@command -v $(ZIP_BIN) >/dev/null 2>&1 || { echo "error: zip が見つかりません (Debian/Ubuntu: apt install zip / macOS: brew install zip)" >&2; exit 1; }
	@$(TAR) --version 2>/dev/null | grep -q 'GNU tar' || { echo "error: GNU tar が必要です (macOS: brew install gnu-tar してから make package TAR=gtar)" >&2; exit 1; }
	@mkdir -p $(DIST_DIR)
	@# 旧アーカイブ・make cross の素バイナリ・中断で残ったステージを一掃して、古い版が混ざらないようにする。
	@rm -rf $(DIST_DIR)/$(BIN_NAME)-* $(DIST_DIR)/SHA256SUMS
	@case "$(VERSION)" in \
		*-dirty) echo "warning: VERSION=$(VERSION) — 作業ツリーが汚れています。クリーンなタグ付きの木で実行してください。" >&2 ;; \
		dev) echo "warning: VERSION=dev — git 情報がありません。リリースには git tag vX.Y.Z を推奨します。" >&2 ;; \
		*-[0-9]*-g[0-9a-f]*) echo "warning: VERSION=$(VERSION) はタグから先行したコミットです。リリースはタグ直上のコミットで実行してください。" >&2 ;; \
		v[0-9]*) : ;; \
		*) echo "$(VERSION)" | grep -Eq '^[0-9a-f]{7,40}$$' \
			&& echo "warning: VERSION=$(VERSION) はコミットハッシュです。リリース前に git tag vX.Y.Z を付けてください。" >&2 || true ;; \
	esac
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		ext=; [ "$$os" = windows ] && ext=.exe; \
		name=$(BIN_NAME)-$(VERSION)-$$os-$$arch; \
		stage=$(DIST_DIR)/$$name; \
		echo ">> $$stage"; \
		rm -rf $$stage; mkdir -p $$stage; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			$(GO) build $(BUILDFLAGS) -o $$stage/$(BIN_NAME)$$ext $(PKG) || exit 1; \
		cp deadman.conf LICENSE $$stage/ || exit 1; \
		sed '/](img\//d' README.md > $$stage/README.md || exit 1; \
		chmod 0755 $$stage/$(BIN_NAME)$$ext; \
		chmod 0644 $$stage/deadman.conf $$stage/README.md $$stage/LICENSE; \
		if [ "$$os" = windows ]; then \
			find $$stage -exec touch -t $(SOURCE_DATE_TOUCH) {} + 2>/dev/null || true; \
			( cd $(DIST_DIR) && rm -f $$name.zip && ZIP= ZIPOPT= TZ=UTC $(ZIP_BIN) -qrX $$name.zip $$name ) || exit 1; \
		else \
			$(TAR) --sort=name --mtime="@$(SOURCE_DATE_EPOCH)" \
				--owner=0 --group=0 --numeric-owner \
				--pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime \
				-C $(DIST_DIR) -cf - $$name | gzip -n > $(DIST_DIR)/$$name.tar.gz || exit 1; \
		fi; \
		rm -rf $$stage; \
	done
	@cd $(DIST_DIR) && \
		if command -v sha256sum >/dev/null 2>&1; then HASH="sha256sum"; \
		elif command -v shasum >/dev/null 2>&1; then HASH="shasum -a 256"; \
		else echo "error: sha256sum/shasum が見つかりません" >&2; exit 1; fi; \
		set --; for f in *.tar.gz *.zip; do [ -e "$$f" ] && set -- "$$@" "$$f"; done; \
		if [ "$$#" -gt 0 ]; then $$HASH "$$@" > SHA256SUMS && echo ">> $(DIST_DIR)/SHA256SUMS"; \
		else echo "warning: アーカイブが無いため SHA256SUMS を生成しません。" >&2; fi

.PHONY: install
install: ## go install で $GOBIN/$GOPATH/bin に導入
	$(GO) install $(BUILDFLAGS) $(PKG)

.PHONY: run
run: ## ローカルで実行 (例: make run ARGS="deadman.conf")
	$(GO) run $(PKG) $(ARGS)

# ---- リント / フォーマッタ -------------------------------------------------
.PHONY: lint
lint: ## golangci-lint でリント (検査のみ)
	$(GOLANGCI) run ./...

.PHONY: lint-fix
lint-fix: ## リントの自動修正可能な指摘を修正
	$(GOLANGCI) run --fix ./...

.PHONY: fmt
fmt: ## フォーマッタ (gci/gofumpt/golines) を適用
	$(GOLANGCI) fmt ./...

.PHONY: fmt-check
fmt-check: ## 整形差分があれば検出 (CI 向け、書き換えなし)
	$(GOLANGCI) fmt --diff ./...

.PHONY: config-verify
config-verify: ## .golangci.yml の妥当性を検証
	$(GOLANGCI) config verify

.PHONY: vet
vet: ## go vet を実行
	$(GO) vet ./...

# ---- テスト ----------------------------------------------------------------
.PHONY: test
test: ## 全テストを実行 (キャッシュ無効)
	$(GO) test -count=1 ./...

.PHONY: test-race
test-race: ## レースディテクタ付きでテスト
	$(GO) test -race -count=1 ./...

.PHONY: test-manual
test-manual: ## 実 ICMP を伴う手動テスト (//go:build manual) を実行
	$(GO) test -tags manual -count=1 ./internal/ping/...

.PHONY: cover
cover: ## カバレッジを計測し関数別サマリを表示
	$(GO) test -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

.PHONY: cover-html
cover-html: cover ## カバレッジを HTML (coverage.html) で出力
	$(GO) tool cover -html=coverage.out -o coverage.html

# ---- 依存関係 / ツール -----------------------------------------------------
.PHONY: tidy
tidy: ## go mod tidy で依存を整理
	$(GO) mod tidy

.PHONY: tidy-check
tidy-check: ## go.mod/go.sum が tidy 済みか検証 (CI 向け)
	$(GO) mod tidy
	@git diff --exit-code go.mod go.sum

.PHONY: tools
tools: ## 開発ツール (golangci-lint) を導入
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

# ---- 集約 / 後始末 ---------------------------------------------------------
.PHONY: check
check: tidy-check fmt-check vet lint test ## CI 相当の全チェックをまとめて実行

.PHONY: clean
clean: ## 生成物を削除
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out coverage.html

# ---- ヘルプ ----------------------------------------------------------------
.PHONY: help
help: ## このヘルプを表示
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
