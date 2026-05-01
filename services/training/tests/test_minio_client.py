"""Tests for adapters/minio_client.py."""

import pytest
from unittest.mock import MagicMock, patch

from training.adapters.minio_client import MinIOStorageClient


class TestMinIOStorageClientLatestVersion:
    def test_no_objects_returns_none(self):
        with patch("training.adapters.minio_client.Minio") as mock_minio_cls:
            mock_client = MagicMock()
            mock_client.bucket_exists.return_value = True
            mock_client.list_objects.return_value = iter([])  # empty
            mock_minio_cls.return_value = mock_client

            client = MinIOStorageClient(
                endpoint="minio:9000",
                access_key="minioadmin",
                secret_key="minioadmin",
                bucket="models",
            )
            latest = client.latest_version("agent-1", "http.latency", "sarima")
            assert latest is None

    def test_returns_highest_version(self):
        with patch("training.adapters.minio_client.Minio") as mock_minio_cls:
            mock_client = MagicMock()
            mock_client.bucket_exists.return_value = True

            # Simulate objects returned by list_objects
            class FakeObject:
                def __init__(self, name):
                    self.object_name = name

            mock_client.list_objects.return_value = iter([
                FakeObject("agent-1__http_latency__sarima__v1/model.onnx"),
                FakeObject("agent-1__http_latency__sarima__v3/model.onnx"),
                FakeObject("agent-1__http_latency__sarima__v2/model.onnx"),
            ])

            mock_minio_cls.return_value = mock_client
            client = MinIOStorageClient(
                endpoint="minio:9000",
                access_key="minioadmin",
                secret_key="minioadmin",
                bucket="models",
            )
            latest = client.latest_version("agent-1", "http.latency", "sarima")
            assert latest == "agent-1__http_latency__sarima__v3"

    def test_empty_series_returns_none(self):
        with patch("training.adapters.minio_client.Minio") as mock_minio_cls:
            mock_client = MagicMock()
            mock_client.bucket_exists.return_value = True
            mock_client.list_objects.return_value = iter([])

            mock_minio_cls.return_value = mock_client
            client = MinIOStorageClient(
                endpoint="minio:9000",
                access_key="minioadmin",
                secret_key="minioadmin",
                bucket="models",
            )
            latest = client.latest_version("unknown-agent", "unknown-metric", "sarima")
            assert latest is None
