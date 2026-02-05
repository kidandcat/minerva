# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Install dependencies
RUN apk add --no-cache gcc musl-dev

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Build main binary
COPY . .
RUN CGO_ENABLED=1 go build -o minerva .

# Build agent binary
RUN CGO_ENABLED=0 go build -o minerva-agent ./agent

# Runtime stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

# Install Claude CLI (required for AI backend)
RUN apk add --no-cache npm && npm install -g @anthropic-ai/claude-code && apk del npm

WORKDIR /app

COPY --from=builder /build/minerva .
COPY --from=builder /build/minerva-agent .

# Create workspace directory
RUN mkdir -p /app/workspace

EXPOSE 8080

CMD ["./minerva"]
