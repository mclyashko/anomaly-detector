"""Model naming utilities.

Each trained model is named deterministically:
    {agent_id}__{metric_name}__{model_type}__{version}

Example:
    agent-1__http_latency_avg_ms__sarima__v3
"""

from __future__ import annotations


def sanitize_metric_name(metric_name: str) -> str:
    """Replace characters that are unsafe for filesystem/S3 key names."""
    return metric_name.replace(".", "_").replace("/", "_").replace(":", "_")


def model_name(agent_id: str, metric_name: str, model_type: str, version: str) -> str:
    """Build a deterministic model artifact name.

    Args:
        agent_id:     Source agent identifier (e.g. "agent-1").
        metric_name:  Metric name (e.g. "http.latency_avg_ms").
        model_type:   Model family (e.g. "sarima").
        version:      Semantic version string (e.g. "v1").

    Returns:
        A namespaced model name, e.g. "agent-1__http_latency_avg_ms__sarima__v1".
    """
    safe_metric = sanitize_metric_name(metric_name)
    return f"{agent_id}__{safe_metric}__{model_type}__{version}"


def parse_model_name(name: str) -> dict[str, str] | None:
    """Parse a model name back into its components.

    Format: {agent_id}__{metric_name}__{model_type}__{version}

    Returns None if the name does not match the expected format.
    """
    parts = name.split("__")
    if len(parts) != 4:
        return None
    agent_id, metric_name, model_type, version = parts
    return {
        "agent_id": agent_id,
        "metric_name": metric_name,
        "model_type": model_type,
        "version": version,
    }
