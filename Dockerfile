FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-w -s" -o answerflow main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/answerflow .
EXPOSE 8080
CMD ["./answerflow"]