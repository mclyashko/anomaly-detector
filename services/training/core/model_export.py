"""ONNX model export for trained anomaly detection models.

SARIMA is not a traditional ML model that sklearn-onnx can export directly.
Instead, we export the forecast function as a simple ONNX model that:
- Input:  a window of recent metric values (1D tensor)
- Output: point forecast + lower_ci + upper_ci (1D tensor of 3 values)

For the MVP, we serialize the SARIMA parameters as JSON metadata embedded in the
ONNX model via a custom producer民_ attribute. The Go Analyzer can load the ONNX
runtime and use the parameters directly for simple linear forecasting.

For a more complete implementation, the full recursive SARIMAX forecast
should be compiled into the ONNX graph using onnxruntime's Python API.
"""

from __future__ import annotations

import json
import logging
from io import BytesIO

import numpy as np

logger = logging.getLogger(__name__)


def export_sarima_to_onnx(
    params: dict,
    window_size: int = 48,
) -> bytes:
    """Export SARIMA parameters as an ONNX model.

    The exported ONNX model is a simple linear forecaster:
        - Input:  [window_size] float values (recent history)
        - Output: [3] float values [forecast, lower_ci, upper_ci]

    The actual SARIMA recursive forecast is approximated with a linear
    AR(1) model inside ONNX for simplicity. The full SARIMAX recursive
    forecast can be added as a custom ONNX operator in a follow-up.

    Args:
        params:      TrainingResult.params dictionary.
        window_size: Number of input history points (default 48 for 2 days at hourly resolution).

    Returns:
        ONNX model serialized as bytes.
    """
    try:
        import onnx
        from onnx import helper, TensorProto
    except ImportError:
        logger.warning("onnx not installed, returning placeholder bytes")
        return b"onnx_placeholder"

    # Serialize SARIMA parameters as JSON for the consumer.
    params_json = json.dumps(params, default=str)

    # Build a minimal ONNX graph that does:
    #   output = ar * input_slice + bias
    # This is a linear approximation; full SARIMAX needs custom op or Python runtime.
    #
    # Graph:
    #   input (float[window_size])
    #   Slice(last_value) -> scalar
    #   Mul(scalar, ar_coeff) -> ar_term
    #   Add(last_value, ar_term) -> forecast
    #   Cast forecast to output

    input_tensor = helper.make_tensor_value_info(
        "history", TensorProto.FLOAT, [window_size]
    )
    output_tensor = helper.make_tensor_value_info(
        "forecast", TensorProto.FLOAT, [3]
    )

    # Node: slice to get the last value (index = window_size - 1)
    slice_node = helper.make_node(
        "Slice",
        inputs=["history"],
        outputs=["last_val"],
        starts=[window_size - 1],
        ends=[window_size],
        axes=[0],
    )

    # Node: multiply last_val by ar coefficient
    ar_tensor = helper.make_tensor(
        "ar_coeff", TensorProto.FLOAT, [], [params.get("ar_params", 0.0)]
    )
    ar_mul_node = helper.make_node(
        "Mul",
        inputs=["last_val", "ar_coeff"],
        outputs=["ar_term"],
    )

    # Node: add last_val + ar_term = forecast
    forecast_add_node = helper.make_node(
        "Add",
        inputs=["last_val", "ar_term"],
        outputs=["point_forecast"],
    )

    # Node: expand forecast to [forecast, lower, upper]
    # Use Identity for point forecast, then Concat for the 3 outputs.
    identity_node = helper.make_node(
        "Identity",
        inputs=["point_forecast"],
        outputs=["forecast_val"],
    )

    # Compute confidence interval half-width from residual std.
    from scipy.stats import norm

    confidence_level = params.get("confidence_level", 0.95)
    z = norm.ppf((1 + confidence_level) / 2)
    residual_std = params.get("residual_std", 1.0)
    half_width = float(z * residual_std)

    half_width_tensor = helper.make_tensor(
        "half_width", TensorProto.FLOAT, [], [half_width]
    )

    # Subtract half_width from forecast for lower bound
    lower_node = helper.make_node(
        "Sub",
        inputs=["point_forecast", "half_width"],
        outputs=["lower_ci"],
    )

    # Add half_width to forecast for upper bound
    upper_node = helper.make_node(
        "Add",
        inputs=["point_forecast", "half_width"],
        outputs=["upper_ci"],
    )

    # Concat to [forecast, lower, upper]
    concat_node = helper.make_node(
        "Concat",
        inputs=["forecast_val", "lower_ci", "upper_ci"],
        outputs=["forecast"],
        axis=0,
    )

    graph_def = helper.make_graph(
        nodes=[
            slice_node,
            ar_mul_node,
            forecast_add_node,
            identity_node,
            lower_node,
            upper_node,
            concat_node,
        ],
        name="sarima_forecast",
        inputs=[input_tensor],
        outputs=[output_tensor],
        initializer=[ar_tensor, half_width_tensor],
        # Store params as a string attribute on the graph.
        value_info=[
            helper.make_tensor_value_info("last_val", TensorProto.FLOAT, [1]),
            helper.make_tensor_value_info("ar_term", TensorProto.FLOAT, [1]),
            helper.make_tensor_value_info("point_forecast", TensorProto.FLOAT, [1]),
            helper.make_tensor_value_info("forecast_val", TensorProto.FLOAT, [1]),
            helper.make_tensor_value_info("lower_ci", TensorProto.FLOAT, [1]),
            helper.make_tensor_value_info("upper_ci", TensorProto.FLOAT, [1]),
        ],
    )
    graph_def.doc_string = params_json

    model_def = helper.make_model(graph_def, opset_imports=[helper.make_opsetid("", 14)])
    model_def.producer_name = "anomaly-detector-training"
    model_def.ir_version = 8

    bio = BytesIO()
    bio.write(onnx._serialize(model_def))  # type: ignore[attr-defined]
    return bio.getvalue()


def load_onnx_model(onnx_bytes: bytes) -> "onnx.ModelProto":
    """Load an ONNX model from bytes."""
    import onnx

    return onnx.load_from_string(onnx_bytes)
