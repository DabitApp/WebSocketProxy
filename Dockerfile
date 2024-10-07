FROM golang:1.23 as build

WORKDIR /src

COPY proxy_src .

RUN go mod download && go mod verify && go build -o proxy main.go


FROM nginx:1.27.1

RUN mkdir /app
COPY ./template/nginx.conf.template /nginx.conf.template
COPY script_src/startup.sh /app
COPY --from=build /src/proxy /app

CMD ["/app/startup.sh"]

EXPOSE 8080
