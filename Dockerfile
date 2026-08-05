# syntax=docker/dockerfile:1.7
FROM golang:1.25-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/tradingmaster \
    ./cmd/tradingmaster

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/tradingmaster /tradingmaster
USER nonroot:nonroot
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/tradingmaster", "-mode", "healthcheck"]

ENTRYPOINT ["/tradingmaster"]
