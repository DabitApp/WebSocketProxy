#!/bin/sh

if [ -z "$PROXY_TARGET" ]; then
    echo "Error: PROXY_TARGET environment variable is not set."
    exit 1
fi

# Perform environment variable substitution
envsubst '$PROXY_TARGET' < /nginx.conf.template > /etc/nginx/nginx.conf

# Set up signal handler *before* starting processes
trap cleanup INT TERM

# Function to handle shutdown
cleanup() {
    echo "Shutting down gracefully..."
    # Send stop signals
    kill -INT $PROXY_PID 2>/dev/null
    kill -INT $SOCKS5_PID 2>/dev/null
    # Nginx is a child process, so 'quit' is correct
    nginx -s quit 2>/dev/null

    # Wait for all background processes to stop
    wait $PROXY_PID $SOCKS5_PID
    echo "All processes stopped"
    exit 0
}

# Start all applications in the background
echo "Starting services..."
/app/proxy -addr=127.0.0.1:58080 -auth=$AUTH_KEY &
PROXY_PID=$!

/app/socks5 &
SOCKS5_PID=$!

nginx -g 'daemon off;' &
NGINX_PID=$!

# Monitor all processes. If any of them dies, trigger cleanup.
while true; do
    # Check if processes are still running
    # `kill -0` just tests if the PID exists
    if ! kill -0 $PROXY_PID 2>/dev/null; then
        echo "Proxy app crashed. Shutting down."
        break
    elif ! kill -0 $SOCKS5_PID 2>/dev/null; then
        echo "SOCKS5 app crashed. Shutting down."
        break
    elif ! kill -0 $NGINX_PID 2>/dev/null; then
        echo "Nginx crashed. Shutting down."
        break
    fi
    
    # Wait for a short time before checking again
    sleep 5
done

# If the loop breaks (due to a crash), call cleanup
cleanup