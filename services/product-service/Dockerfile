FROM golang:1.22-alpine AS builder
ARG BUILD_DATE
ARG GIT_SHA
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o product-service ./main.go

FROM alpine:latest
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /app/product-service /product-service
ENV TZ=UTC
EXPOSE 8081
ENTRYPOINT ["/product-service"]
