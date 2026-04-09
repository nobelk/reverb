FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /reverb ./cmd/reverb

FROM alpine:3.21
RUN apk --no-cache add ca-certificates && \
    addgroup -S appuser && adduser -S appuser -G appuser
COPY --from=builder /reverb /usr/local/bin/reverb
USER appuser
EXPOSE 8080 9090 9091 9100
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["reverb"]
CMD ["--http-addr", ":8080"]
