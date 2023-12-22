FROM golang:1.21.5

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY *.go .
RUN CGO_ENABLED=0 go build -o webhook main.go

FROM alpine:3.18

COPY --from=0 /app/webhook /webhook

ENTRYPOINT ["/webhook"]