# deadman の開発用 Makefile。`make` でターゲットの一覧を出す。

# `make tools` で入れる golangci-lint の版。.golangci.yml はこの版に合わせて書いている。
GOLANGCI_VERSION ?= v2.14.0

# macOS では GNU tar を `make package TAR=gtar` で指定できる。
# ZIP は Info-ZIP が環境変数 ZIP を既定オプションとして読むため、別名にする。
TAR     ?= tar
ZIP_BIN ?= zip

# バイナリに埋め込み、アーカイブ名にも使う版。リリースでは CI がタグを渡す。
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILDFLAGS := -trimpath -ldflags '-s -w -X main.version=$(VERSION)'

# リリースする対象と、go vet と golangci-lint を開発機に加えて回す対象。後者は OS 別のファイルと 32bit を見るため。
PLATFORMS       := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
CROSS_PLATFORMS := linux/386 linux/arm darwin/arm64 windows/amd64

# アーカイブを決定的にするため、中身の時刻をコミット時刻にそろえる。
SOURCE_DATE_EPOCH ?= $(shell git log -1 --pretty=%ct)

.DEFAULT_GOAL := help
.PHONY: help build check lint vet test fix cover package tools clean

help: ## このヘルプを出す
	@awk 'BEGIN {FS = ":.*## "} /^[a-z]+:.*## / {printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## このマシン向けに bin/ へビルドする
	go build $(BUILDFLAGS) -o bin/ ./cmd/deadman

check: lint vet test ## コミット前の検査をすべて通す

# run は設定の綴り誤りを黙って無視し、開発機の OS 向けのファイルしか見ないので、
# config verify と fmt --diff を回し、run は CROSS_PLATFORMS でも回す (manual タグは設定で有効)。
lint: ## go.mod・設定・整形・リントを検査する (書き換えない。他の OS と 32bit も)
	go mod tidy -diff
	golangci-lint config verify
	golangci-lint fmt --diff ./...
	golangci-lint run ./...
	@for p in $(CROSS_PLATFORMS); do \
		echo ">> golangci-lint run $$p"; \
		GOOS=$${p%/*} GOARCH=$${p#*/} CGO_ENABLED=0 golangci-lint run ./... || exit 1; \
	done

vet: ## 開発機と CROSS_PLATFORMS で go vet する (manual タグのテストも含む)
	go vet -tags manual ./...
	@for p in $(CROSS_PLATFORMS); do \
		echo ">> go vet $$p"; \
		GOOS=$${p%/*} GOARCH=$${p#*/} go vet -tags manual ./... || exit 1; \
	done

test: ## テストを実行する (例: TEST_ARGS='-race -run=TestReload')
	go test -race -count=1 $(TEST_ARGS) ./...

fix: ## lint の指摘のうち機械的に直せるものを直す
	go mod tidy
	golangci-lint fmt ./...
	golangci-lint run --fix ./...

cover: ## カバレッジを coverage.out に取り、関数ごとに出す
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

package: ## リリース用のアーカイブと SHA256SUMS を dist/ に作る (GNU tar・gzip・zip が要る)
	@set -e; \
		command -v $(ZIP_BIN) >/dev/null 2>&1 || { echo "error: zip が見つかりません" >&2; exit 1; }; \
		command -v gzip >/dev/null 2>&1 || { echo "error: gzip が見つかりません" >&2; exit 1; }; \
		$(TAR) --version 2>/dev/null | grep -q 'GNU tar' || { echo "error: GNU tar が必要です (macOS: make package TAR=gtar)" >&2; exit 1; }; \
		if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then \
			echo "error: sha256sum または shasum が必要です" >&2; exit 1; \
		fi; \
		touch_time=$$(TZ=UTC date -u -d @$(SOURCE_DATE_EPOCH) +%Y%m%d%H%M.%S 2>/dev/null || TZ=UTC date -u -r $(SOURCE_DATE_EPOCH) +%Y%m%d%H%M.%S 2>/dev/null) || { \
			echo "error: SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) を日時に変換できません" >&2; exit 1; \
		}; \
		mkdir -p dist; \
		rm -f dist/SHA256SUMS; \
		set --; \
		for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; name=deadman-$(VERSION)-$$os-$$arch; \
		echo ">> dist/$$name"; \
		rm -rf "dist/$$name"; \
		rm -f "dist/$$name.zip" "dist/$$name.tar.gz"; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(BUILDFLAGS) -o dist/$$name/ ./cmd/deadman; \
		cp deadman.conf LICENSE dist/$$name/; \
		sed '/](img\//d' README.md > dist/$$name/README.md; \
		chmod -R u=rwX,go=rX dist/$$name; \
		TZ=UTC touch -t "$$touch_time" dist/$$name dist/$$name/*; \
		if [ $$os = windows ]; then \
			(cd dist && ZIP= ZIPOPT= TZ=UTC $(ZIP_BIN) -qrX "$$name.zip" "$$name"); \
			set -- "$$@" "$$name.zip"; \
		else \
			$(TAR) --sort=name --owner=0 --group=0 --numeric-owner \
				--pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime \
				--use-compress-program='gzip -n' -C dist -cf "dist/$$name.tar.gz" "$$name"; \
			set -- "$$@" "$$name.tar.gz"; \
		fi; \
		rm -rf "dist/$$name"; \
		done; \
		if command -v sha256sum >/dev/null 2>&1; then \
			(cd dist && sha256sum "$$@" > SHA256SUMS); \
		else \
			(cd dist && shasum -a 256 "$$@" > SHA256SUMS); \
		fi

tools: ## GOLANGCI_VERSION の golangci-lint を入れる
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

clean: ## 生成物を消す
	rm -rf bin dist coverage.out
