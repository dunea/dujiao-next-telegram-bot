# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dujiao-bot ./cmd/bot

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S bot \
    && adduser -S -G bot bot
USER bot
COPY --from=build /out/dujiao-bot /usr/local/bin/dujiao-bot
EXPOSE 8444
ENTRYPOINT ["/usr/local/bin/dujiao-bot"]
