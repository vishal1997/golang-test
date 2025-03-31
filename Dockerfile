# Stage 1: Build
FROM golang:1.23.5 as builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Cross-compile the Go binary for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o main .

# Stage 2: Run
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/main .

# Ensure the binary has executable permissions
RUN chmod +x main

EXPOSE 8080

CMD ["./main"]
