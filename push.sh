#!/bin/bash

if [ -f .env ]; then
    echo "Loading environment variables from .env"
    set -a
    . .env
    set +a
else
    echo "No .env file found. Using environment variables as is."
fi

docker push asia.gcr.io/$PROJECT_ID/$IMAGE_NAME 
