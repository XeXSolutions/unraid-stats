# Build stage
FROM golang:1.21-alpine3.19 AS builder

WORKDIR /app

# Install build dependencies
RUN apk update && \
    apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy the rest of the source code
COPY . .

# Build the application with CGO disabled
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o unraid-stats ./cmd/main.go

# Final stage
FROM alpine:3.19

# Add comprehensive labels for container metadata
LABEL maintainer="XeXSolutions"
LABEL org.opencontainers.image.source="https://github.com/XeXSolutions/unraid-stats"
LABEL org.opencontainers.image.description="Unraid System Statistics Viewer"
LABEL org.opencontainers.image.licenses=MIT
LABEL org.opencontainers.image.url="https://github.com/XeXSolutions/unraid-stats"
LABEL org.opencontainers.image.documentation="https://github.com/XeXSolutions/unraid-stats/blob/main/README.md"
LABEL com.unraid.template.icon="https://raw.githubusercontent.com/XeXSolutions/unraid-stats/main/images/logo.png"
LABEL com.unraid.template.url="https://raw.githubusercontent.com/XeXSolutions/unraid-stats/main/my-unraid-stats.xml"
LABEL com.unraid.template.overview="A modern, real-time system monitoring dashboard for Unraid servers. Features include CPU, memory, network monitoring, and array status tracking with a clean, responsive interface."
LABEL com.unraid.template.category="Tools: System:Monitoring"
LABEL com.unraid.template.webui="http://[IP]:[PORT:8085]/"
LABEL com.unraid.template.support="https://github.com/XeXSolutions/unraid-stats/issues"

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /app/unraid-stats .
# Copy web files
COPY --from=builder /app/web ./web

# Expose port 8085
EXPOSE 8085

# Run the binary
CMD ["./unraid-stats"]