# Build stage - using Go 1.23 (latest stable on Docker Hub)
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files and adjust version for container build
COPY go.mod go.sum ./
RUN sed -i 's/go 1.25.8/go 1.23/' go.mod && go mod download

# Copy source code
COPY . .

# Build binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o sentinel-agent .

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Install CA certificates for HTTPS
RUN apk add --no-cache ca-certificates

# Copy binary from builder
COPY --from=builder /app/sentinel-agent .

# Create non-root user
RUN adduser -D -u 1000 sentinel
USER sentinel

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/summary || exit 1

ENTRYPOINT ["./sentinel-agent"]
