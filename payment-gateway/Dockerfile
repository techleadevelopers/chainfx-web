# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/api ./cmd/api

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --uid 10001 appuser

WORKDIR /app

COPY --from=builder /out/api /app/api
RUN mkdir -p /app/secrets \
    && chown -R appuser:appuser /app

ENV APP_ENV=production
ENV PORT=8080
ENV TZ=UTC
ENV GODEBUG=x509negativeserial=1

EXPOSE 8080

USER appuser

CMD ["/app/api"]
