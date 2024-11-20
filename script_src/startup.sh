#!/bin/sh

if [ -z "$PROXY_TARGET" ]; then
    echo "Error: PROXY_TARGET environment variable is not set."
    exit 1
fi

# Perform environment variable substitution
envsubst '$PROXY_TARGET' < /nginx.conf.template > /etc/nginx/nginx.conf

# Start the Go application in the background
/app/proxy -addr=127.0.0.1:58080 -auth=$AUTH_KEY &
/app/socks5 &


echo "start nginx"
# Start Nginx in the foreground
nginx -g 'daemon off;'
