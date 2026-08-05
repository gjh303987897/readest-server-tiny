FROM golang:1.25-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=1
RUN apk add --no-cache build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/readest-server ./cmd/readest-server

FROM alpine:3.21
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
