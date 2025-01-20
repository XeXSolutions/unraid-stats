# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o unraid-stats cmd/main.go

# Final stage
FROM alpine:latest

LABEL org.opencontainers.image.source="https://github.com/yourusername/unraid-stats"
LABEL org.opencontainers.image.description="Unraid System Statistics Viewer"
LABEL org.opencontainers.image.licenses=MIT

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/unraid-stats .
# Copy web files
COPY --from=builder /app/web ./web

# Expose port 8080
EXPOSE 8080

# Run the binary
CMD ["./unraid-stats"] 