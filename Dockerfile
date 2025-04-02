# ---- Builder Stage ----
# Use an official Go image with Alpine base (Updated to 1.23)
FROM golang:1.23-alpine AS builder

# Set working directory
WORKDIR /app

# Install CGO dependencies required by TagLib using apk
# build-base: Provides make, gcc, etc.
# pkgconf: Equivalent to pkg-config
# zlib-dev: zlib development files
# taglib-dev: TagLib development files
RUN apk update && \
    apk add --no-cache \
    build-base \
    pkgconf \
    zlib-dev \
    taglib-dev

# Copy go module files
COPY go.mod go.sum ./

# Download Go module dependencies
# This leverages Docker layer caching
RUN go mod download

# Copy the entire project source code
COPY . .

# Build the Go application with CGO enabled for Linux/amd64
# Target the main package in cmd/beatportdl
# Output the binary to /app/beatportdl
# Use -ldflags="-s -w" to strip debug information and reduce binary size
# Explicitly set CGO flags using pkg-config
# Note: Alpine uses musl libc, which might affect CGO builds differently
# Target arm64 since the build host is ARM
# Rely on CGO directives within internal/taglib/taglib.go for C flags
RUN CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=gcc \
    go build -ldflags="-s -w" -o /app/beatportdl ./cmd/beatportdl/

# ---- Runtime Stage ----
# Use a minimal Alpine image
FROM alpine:3.19 AS runtime

# Set working directory
WORKDIR /app

# Install runtime dependencies using apk
# taglib: TagLib runtime library
# zlib: zlib runtime library
# ca-certificates: For HTTPS requests
RUN apk update && \
    apk add --no-cache \
    taglib \
    zlib \
    ca-certificates && \
    rm -rf /var/cache/apk/*

# Copy the compiled binary from the builder stage
COPY --from=builder /app/beatportdl /app/beatportdl

# Copy the example config file for reference
COPY .env.example /app/beatportdl-config.yml.example

# Set the entrypoint for the container
# The application will look for beatportdl-config.yml and beatportdl-credentials.json in /app
ENTRYPOINT ["/app/beatportdl"]

# Note: You will need to mount your actual beatportdl-config.yml and
# beatportdl-credentials.json into the /app directory when running the container.
# Example:
# docker run -d --name beatportdl-bot \
#   -v $(pwd)/beatportdl-config.yml:/app/beatportdl-config.yml \
#   -v $(pwd)/beatportdl-credentials.json:/app/beatportdl-credentials.json \
#   -v $(pwd)/downloads:/app/downloads \
#   your-dockerhub-username/beatportdl:latest
