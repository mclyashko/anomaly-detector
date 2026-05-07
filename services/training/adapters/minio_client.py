"""MinIO / S3 adapter — implements ModelStoragePort with versioning."""

from __future__ import annotations

import io
import json
import logging
import re
from typing import Any

from minio import Minio

from ..ports.storage_port import ModelStoragePort
from ..core import naming

logger = logging.getLogger(__name__)


class MinIOStorageClient:
    """MinIO client that stores model artifacts with versioned folder structure.

    Storage layout:
        {bucket}/
            {agent_id}__{metric_name}__{model_type}__{version}/
                model.onnx
                metadata.json
    """

    def __init__(
        self,
        endpoint: str,
        access_key: str,
        secret_key: str,
        bucket: str,
    ) -> None:
        """Initialize MinIO client.

        Args:
            endpoint:   MinIO server address (e.g. "minio:9000").
            access_key:  MinIO access key.
            secret_key: MinIO secret key.
            bucket:     Bucket name for model storage (e.g. "models").
        """
        self._client = Minio(
            endpoint,
            access_key=access_key,
            secret_key=secret_key,
            secure=False,
        )
        self._bucket = bucket
        self._ensure_bucket()

    def _ensure_bucket(self) -> None:
        """Create the bucket if it does not already exist."""
        if not self._client.bucket_exists(self._bucket):
            self._client.make_bucket(self._bucket)
            logger.info("created bucket: %s", self._bucket)

    def upload(
        self,
        model_name: str,
        artifact: bytes,
        metadata: dict[str, Any],
    ) -> str:
        """Upload a model artifact and metadata to MinIO.

        Creates a versioned folder:
            {bucket}/{model_name}/model.onnx
            {bucket}/{model_name}/metadata.json
        """
        folder = model_name  # the full name is used as the folder prefix
        onnx_key = f"{folder}/model.onnx"
        meta_key = f"{folder}/metadata.json"

        # Upload ONNX artifact.
        bio = io.BytesIO(artifact)
        self._client.put_object(
            self._bucket,
            onnx_key,
            bio,
            length=len(artifact),
            content_type="application/octet-stream",
        )
        logger.debug("uploaded ONNX: %s/%s", self._bucket, onnx_key)

        # Upload metadata as JSON.
        meta_bytes = json.dumps(metadata, indent=2, default=str).encode("utf-8")
        meta_bio = io.BytesIO(meta_bytes)
        self._client.put_object(
            self._bucket,
            meta_key,
            meta_bio,
            length=len(meta_bytes),
            content_type="application/json",
        )
        logger.debug("uploaded metadata: %s/%s", self._bucket, meta_key)

        return f"s3://{self._bucket}/{folder}"

    def latest_version(
        self,
        agent_id: str,
        metric_name: str,
        model_type: str,
    ) -> str | None:
        """Find the latest model version for the given (agent_id, metric_name, type).

        Searches MinIO for folders matching:
            {agent_id}__{metric_sanitized}__{model_type}__v{N}

        Returns the full folder name of the latest version, or None if no model exists.
        """
        safe_metric = naming.sanitize_metric_name(metric_name)
        prefix = f"{agent_id}__{safe_metric}__{model_type}__"

        # List objects with the given prefix.
        try:
            objects = self._client.list_objects(
                self._bucket,
                prefix=prefix,
                recursive=True,
            )
        except Exception:
            logger.exception("failed to list objects for prefix %s", prefix)
            return None

        # Collect all version strings found.
        versions: list[int] = []
        for obj in objects:
            # obj.object_name is like "agent-1__http_latency__sarima__v3/model.onnx"
            basename = obj.object_name.split("/")[0]  # folder part: "agent-1__http_latency__sarima__v3"
            # Regex extracts version number from folder name: "__v" followed by digits at end of string.
            # E.g. "agent-1__http_latency__sarima__v3" -> version 3.
            m = re.search(r"__v(\d+)$", basename)
            if m:
                versions.append(int(m.group(1)))

        if not versions:
            return None

        latest_n = max(versions)
        return f"{prefix}v{latest_n}"
