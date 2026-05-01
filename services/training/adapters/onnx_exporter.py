"""ONNX exporter adapter — thin wrapper around core/model_export."""

from __future__ import annotations

# re-export for adapters package namespace
from training.core.model_export import export_sarima_to_onnx  # noqa: F401
