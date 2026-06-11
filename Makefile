# deadman の開発用 Makefile。
# `make` または `make help` で利用可能なターゲット一覧を表示する。

# ---- 設定 ------------------------------------------------------------------
GO            ?= go
GOLANGCI      ?= golangci-lint
# golangci-lint を `make tools` で入れる際のバージョン (現行 v2 系)。
GOLANGCI_VERSION ?= v2.12.2

BIN_NAME  := deadman
PKG       := ./cmd/deadman
BIN_DIR   := bin
DIST_DIR  := dist

# 実行ファイルの拡張子 (Windows は .exe、その他は空)。go env GOEXE は GOOS の上書きにも追従する。
EXE       := $(shell $(GO) env GOEXE)

# git から導出するバージョン文字列 (タグが無ければコミットハッシュ、未コミット差分は -dirty)。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

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
