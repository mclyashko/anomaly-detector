#!/bin/sh
# Upload pre-trained models from /models/ to MinIO.
# Uses mc (MinIO Client) to upload directly.

set -e

# Set up mc alias
mc alias set local http://minio:9000 minioadmin minioadmin

# Ensure bucket exists
mc mb -p local/models 2>/dev/null || true

# Upload each model folder
for dir in /models/*/; do
    name=$(basename "$dir")
    echo "Uploading model: $name"
    mc cp "$dir/metadata.json" local/models/$name/metadata.json
    echo "  uploaded $name/metadata.json"
    if [ -f "$dir/model.onnx" ]; then
        mc cp "$dir/model.onnx" local/models/$name/model.onnx
        echo "  uploaded $name/model.onnx"
    fi
done

echo "Models in bucket:"
mc ls local/models/ --recursive
echo "Done."
