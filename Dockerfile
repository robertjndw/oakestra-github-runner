# syntax=docker/dockerfile:1
#
# One image: the Go binary serves the API and the embedded dashboard bundle.
#
# @oakestra/dashboard-sdk and @oakestra/ui come from GitHub Packages, which needs a token with
# read:packages even for public packages. Pass it as a build secret so it stays out of the image:
#
#   NODE_AUTH_TOKEN=$(gh auth token) docker buildx build \
#     --secret id=node_auth_token,env=NODE_AUTH_TOKEN -t oakestra-github-runner .

# Both build stages produce platform-independent or cross-compiled output, so they run on the
# build host's platform instead of under emulation.
FROM --platform=$BUILDPLATFORM node:24-slim AS ui
RUN npm install --global pnpm@12.16.1
# pnpm ignores registry tokens in the repo's .npmrc, so it goes in the user config. This only
# writes the placeholder, the real value comes from the secret mounted during install.
RUN printf '//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}\n' > /root/.npmrc
WORKDIR /work/ui
COPY ui/package.json ui/pnpm-lock.yaml ui/pnpm-workspace.yaml ui/.npmrc ./
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    --mount=type=secret,id=node_auth_token,env=NODE_AUTH_TOKEN,required=true \
    pnpm install --frozen-lockfile
COPY ui/ ./
# The dashboard refuses a bundle whose id differs from the addon id the engine reports. That id
# is "github-runner" from the official catalog and "local--github-runner" when the definition is
# registered locally, so local builds override this.
ARG ADDON_ID=github-runner
RUN ADDON_ID=$ADDON_ID pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=ui /work/ui/dist/ internal/webui/dist/
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/ghrunner ./cmd/ghrunner
# A named volume takes its ownership from the image's directory on first use, so the data
# directory has to exist in the image and belong to the runtime user.
RUN mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ghrunner /ghrunner
COPY --from=build --chown=65532:65532 /out/data /data
# Addon UI contract: plain HTTP on port 80, reachable as addonui-<id> on the dashboard network.
# Docker lets unprivileged users bind low ports inside a container.
EXPOSE 80
ENTRYPOINT ["/ghrunner"]
