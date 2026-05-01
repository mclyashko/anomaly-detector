"""Port for model artifact storage (MinIO / S3)."""

from __future__ import annotations

from typing import Protocol


class ModelStoragePort(Protocol):
    """Port for uploading and versioning model artifacts."""

    def upload(
        self,
        model_name: str,
        artifact: bytes,
        metadata: dict,
    ) -> str:
        """Upload a model artifact and its metadata.

        Args:
            model_name: Full model name (e.g. "agent-1__http_latency__sarima__v1").
            artifact:   Raw bytes of the model file (ONNX).
            metadata:  JSON-serializable metadata dict.

        Returns:
            The storage path/URL of the uploaded artifact.
        """
        ...

    def latest_version(
        self,
        agent_id: str,
        metric_name: str,
        model_type: str,
    ) -> str | None:
        """Find the latest versioned model for the given pair.

        Args:
            agent_id:    Source agent identifier.
            metric_name: Metric name.
            model_type:  Model type (e.g. "sarima").

        Returns:
            The full model_name of the latest version (e.g. "agent-1__http_latency__sarima__v3"),
            or None if no model exists for this pair.
        """
        ...
