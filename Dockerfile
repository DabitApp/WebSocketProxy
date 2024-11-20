ARG GOLANG_VERSION="1.16.5"

FROM golang:$GOLANG_VERSION-alpine as builder-1
RUN apk --no-cache add tzdata
WORKDIR /go/src/github.com/serjs/socks5
COPY socks5-server .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-s' -o ./socks5


FROM golang:1.23 as builder-2

WORKDIR /src

COPY proxy_src .

RUN go mod download && go mod verify && go build -o proxy main.go


FROM nginx:1.27.1

RUN mkdir /app
COPY ./template/nginx.conf.template /nginx.conf.template
COPY script_src/startup.sh /app
COPY --from=builder-2 /src/proxy /app
COPY --from=builder-1 /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder-1 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder-1 /go/src/github.com/serjs/socks5/socks5 /app

CMD ["/app/startup.sh"]

EXPOSE 8080
