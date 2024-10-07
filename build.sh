#!/bin/bash

if [ -f .env ]; then
    echo "Loading environment variables from .env"
    set -a
    . .env
    set +a
else
    echo "No .env file found. Using environment variables as is."
fi

if [ -z "$LOCAL_IMAGE_NAME" ]; then
    LOCAL_IMAGE_NAME=nginx-test-dabit
fi

docker build -t  $LOCAL_IMAGE_NAME .
