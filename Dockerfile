### Stage One
FROM golang:1.19-alpine as build-golang

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -o bin/booty -ldflags "-X main.version=$BOOTY_VERSION -X main.timestamp=$BOOTY_TIMESTAMP" cmd/main.go


### Stage Two
FROM node:lts-alpine as build-web
WORKDIR /app
COPY web/package*.json ./
RUN npm install
COPY web/ .
RUN npm run build

### Final Stage
FROM gcr.io/distroless/base-debian10

COPY --from=0 /app/bin/booty /
COPY --from=1 /app/dist/ /web/dist/

ENTRYPOINT [ "/booty" ]