FROM golang:1.16-alpine

WORKDIR /app

COPY . .

RUN pwd && ls && CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -o bin/booty cmd/main.go

FROM gcr.io/distroless/base-debian10

COPY --from=0 /app/bin/booty /

ENTRYPOINT [ "/booty" ]