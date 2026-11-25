VERSION ?= dev
ADDON_ID ?= github-runner

.PHONY: build ui test lint run image

build: ui
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/ghrunner ./cmd/ghrunner

# Builds the dashboard bundle and copies it to where the binary embeds it from.
ui:
	cd ui && ADDON_ID=$(ADDON_ID) pnpm install --frozen-lockfile && ADDON_ID=$(ADDON_ID) pnpm build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R ui/dist/. internal/webui/dist/

test:
	go vet ./...
	go test -race ./...
	cd ui && pnpm typecheck && pnpm test

lint:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...

# Local backend without a dashboard in front of it. GHR_AUTH=off treats every caller as admin.
run:
	GHR_AUTH=off GHR_LISTEN=:8080 GHR_DATA_DIR=./data go run ./cmd/ghrunner

image:
	NODE_AUTH_TOKEN=$$(gh auth token) docker buildx build \
		--secret id=node_auth_token,env=NODE_AUTH_TOKEN \
		--build-arg VERSION=$(VERSION) --build-arg ADDON_ID=$(ADDON_ID) \
		-t oakestra-github-runner:$(VERSION) .
