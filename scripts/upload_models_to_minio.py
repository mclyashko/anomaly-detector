#!/usr/bin/env python3
"""Upload pre-trained models from infra/models/ to MinIO.

This script runs as a Docker init container before other services start.
It uploads all model folders from /models/ (mounted from infra/models/)
into the MinIO 'models' bucket.

Usage:
    python3 upload_models.py --endpoint minio:9000 \
                              --access-key minioadmin \
                              --secret-key minioadmin \
                              --bucket models \
                              --models-dir /models
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description="Upload pre-trained models to MinIO")
    parser.add_argument("--endpoint", default=os.environ.get("MINIO_ENDPOINT", "minio:9000"))
    parser.add_argument("--access-key", default=os.environ.get("MINIO_ACCESS_KEY", "minioadmin"))
    parser.add_argument("--secret-key", default=os.environ.get("MINIO_SECRET_KEY", "minioadmin"))
    parser.add_argument("--bucket", default=os.environ.get("MINIO_BUCKET", "models"))
    parser.add_argument("--models-dir", default="/models")
    args = parser.parse_args()

    try:
        from minio import Minio
    except ImportError:
        print("minio module not installed, skipping model upload", file=sys.stderr)
        sys.exit(0)

    client = Minio(
        args.endpoint,
        access_key=args.access_key,
        secret_key=args.secret_key,
        secure=False,
    )

    # Ensure bucket exists.
    if not client.bucket_exists(args.bucket):
        client.make_bucket(args.bucket)
        print(f"created bucket: {args.bucket}")

    models_dir = Path(args.models_dir)
    if not models_dir.exists():
        print(f"models dir {models_dir} does not exist, skipping upload")
        return

    for model_folder in sorted(models_dir.iterdir()):
        if not model_folder.is_dir():
            continue
        model_name = model_folder.name  # e.g. "agent-test__test.signal__sarima__v1"
        print(f"uploading model: {model_name}")

        # Upload model.onnx if exists.
        onnx_path = model_folder / "model.onnx"
        if onnx_path.exists():
            with open(onnx_path, "rb") as f:
                data = f.read()
            from io import BytesIO
            bio = BytesIO(data)
            client.put_object(
                args.bucket,
                f"{model_name}/model.onnx",
                bio,
                length=len(data),
                content_type="application/octet-stream",
            )
            print(f"  uploaded {model_name}/model.onnx ({len(data)} bytes)")

        # Upload metadata.json if exists.
        meta_path = model_folder / "metadata.json"
        if meta_path.exists():
            with open(meta_path, "rb") as f:
                data = f.read()
            from io import BytesIO
            bio = BytesIO(data)
            client.put_object(
                args.bucket,
                f"{model_name}/metadata.json",
                bio,
                length=len(data),
                content_type="application/json",
            )
            print(f"  uploaded {model_name}/metadata.json ({len(data)} bytes)")

    print("model upload complete")


if __name__ == "__main__":
    main()
