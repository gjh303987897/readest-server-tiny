ARG NODE_IMAGE=node:24-alpine
ARG GO_IMAGE=golang:1.25-alpine
ARG ALPINE_IMAGE=alpine:3.21

FROM ${NODE_IMAGE} AS admin-build

ARG NPM_REGISTRY=https://registry.npmmirror.com
WORKDIR /src/web/admin
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY web/admin ./
RUN npm run build

FROM ${GO_IMAGE} AS build

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=1
RUN apk add --no-cache build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=admin-build /src/web/admin/dist ./web/admin/dist
RUN go build -trimpath -ldflags="-s -w" -o /out/readest-server ./cmd/readest-server

FROM ${ALPINE_IMAGE}
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S readest \
    && adduser -S -G readest -h /app readest \
    && mkdir -p /app/data \
    && chown -R readest:readest /app

WORKDIR /app
COPY --from=build /out/readest-server /usr/local/bin/readest-server
USER readest
EXPOSE 8080
VOLUME ["/app/data"]
ENTRYPOINT ["readest-server"]
