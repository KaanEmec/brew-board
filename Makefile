.PHONY: build test lint fmt fmt-check vet check screenshots

build:
	go build -o bin/brewboard ./cmd/brewboard

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

check: fmt-check vet lint test

# README screenshots: render each screen from sample data (Homebrew is never
# run), then turn the ANSI frames into PNGs with charmbracelet/freeze. Frames
# go in on stdin: freeze reads stdin whenever it is not a terminal. The NL font
# keeps "==>" and "->" unligatured; sips halves freeze's 4x PNGs to 2x.
FREEZE ?= github.com/charmbracelet/freeze@v0.2.2
SCREENS := list details review running receipt

screenshots:
	go run ./tools/screenshots
	@for s in $(SCREENS); do \
		go run $(FREEZE) --language ansi --output docs/screenshots/$$s.png \
			--window --theme charm --font.family "JetBrains Mono NL" --font.size 14 --line-height 1.25 --padding 20,30 \
			--margin 30 --border.radius 10 --border.width 1 --border.color "#3a3a40" \
			--shadow.blur 16 --shadow.y 8 --background "#1b1b1f" < docs/screenshots/$$s.ansi || exit 1; \
		sips --resampleWidth 2090 docs/screenshots/$$s.png >/dev/null || exit 1; \
	done
