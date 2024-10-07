#!/bin/bash

if [ -f .env ]; then
    echo "Loading environment variables from .env"
    set -a
    . .env
    set +a
else
    echo "No .env file found. Using environment variables as is."
fi

docker build -t asia.gcr.io/$GCP_PROJECT_ID/$GCP_IMAGE_NAME .
