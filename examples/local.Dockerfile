#Dockerfile for building the controller and webhook images locally
FROM golang:1.24 AS builder

WORKDIR /app
COPY ../go.mod go.sum ./
RUN go mod download
COPY ../internal ./internal
COPY ../cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/controller-bin ./cmd

FROM alpine:latest
COPY --from=builder /app/controller-bin /usr/local/bin/manager
EXPOSE 9443

ENTRYPOINT ["/usr/local/bin/manager"]
