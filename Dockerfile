#docker build -t fake_downloader .
#docker run -d -p 8084:8084 -e PORT=8084 -e ADDR=127.0.0.1:63219 --name my_service fake_downloader


# Build stage
FROM golang:1.18-alpine AS builder

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o fake_downloader .

# Final stage
FROM alpine:latest

# Install python3 for the reannounce script
RUN apk --no-cache add python3

WORKDIR /root/

# Copy the binary from builder
COPY --from=builder /app/fake_downloader .

# Environment variables for parameters
ENV PORT=8084
ENV ADDR=127.0.0.1:63219

# Expose the port
EXPOSE ${PORT}

# Run the application with parameters from environment variables
CMD ["sh", "-c", "./fake_downloader --port=${PORT} --addr=${ADDR}"]
