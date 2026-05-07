"""SARIMA inference — вычисляет прогноз и доверительные интервалы для обнаружения аномалий.

Модель использует упрощённую SARIMA(1,0,1)(1,0,1) с одной сезонной компонентой.
Идея: если сигнал ведёт себя предсказуемо (есть тренд и сезонность),
то резкое отклонение от прогноза — аномалия.
"""

from __future__ import annotations

import math
from scipy.special import erfcinv


def norm_quantile(p: float) -> float:
    """Квантиль стандартного нормального распределения (обратная функция CDF).

    Возвращает z-score такой, что P(Z < z) = p для стандартной нормальной величины Z.
    Используется для построения доверительных интервалов.

    Метод: начальное приближение через erfcinv (обратная функция ошибок),
    затем уточнение методом Ньютона для точности до 1e-12.
    """
    if p <= 0:
        return float("-inf")
    if p >= 1:
        return float("inf")
    if p == 0.5:
        return 0.0
    # Стандартное нормальное распределение симметрично вокруг 0
    if p < 0.5:
        return -norm_quantile(1 - p)

    # Начальное приближение: используем erfcinv для быстрого z-score
    # erfcinv(y) ≈ norm.ppf(1 - y/2) для y в (0, 2)
    x = erfcinv(2 * (1 - p)) * math.sqrt(2)

    # Уточнение по Ньютону: решаем cdf(x) - p = 0
    # Newton refinement для максимальной точности
    for _ in range(10):
        # CDF стандартного нормального: Φ(x) = 0.5 * (1 + erf(x/sqrt(2)))
        cdf = 0.5 * (1 + math.erf(x / math.sqrt(2)))
        # PDF стандартного нормального: φ(x) = exp(-x²/2) / sqrt(2π)
        pdf = math.exp(-x * x / 2) / math.sqrt(2 * math.pi)
        delta = (cdf - p) / pdf  # Ньютон: x_new = x - f(x)/f'(x)
        x -= delta
        if abs(delta) < 1e-12:
            break
    return x


def evaluate_anomaly(
    history: list[float],
    value: float,
    ar_params: float,
    ma_params: float,
    seasonal_ar_params: float,
    seasonal_ma_params: float,
    residual_std: float,
    seasonality_period: int,
    confidence_level: float = 0.95,
) -> dict:
    """Определяет, является ли текущее значение аномальным по сравнению с историей.

    Формула прогноза — упрощённая SARIMA(1,0,1)(1,0,1):

        прогноз = последнее
               + AR₁ × (последнее - предпоследнее)    # краткосрочный тренд
               + MA₁ × (последнее - предпоследнее)    # сглаживание
               + SAR₁ × (последнее - значение_S_назад) # сезонный тренд
               + SMA₁ × (последнее - значение_S_назад) # сезонное сглаживание

    Если value выходит за пределы [прогноз ± z × residual_std],
    где z — квантиль нормального распределения для заданного confidence_level,
    считаем это аномалией.

    Args:
        history: История значений (от oldest к newest). Нужно минимум 2,
                 а для сезонной компоненты — seasonality_period + 1.
        value: Текущее значение для проверки.
        ar_params: Коэффициент авторегрессии AR(1) — определяет силу краткосрочного тренда.
        ma_params: Коэффициент скользящего среднего MA(1).
        seasonal_ar_params: Сезонный AR коэффициент — реагирует на отклонение
                             от значения S периодов назад.
        seasonal_ma_params: Сезонный MA коэффициент.
        residual_std: Стандартное отклонение остатков модели.
                      Определяет ширину доверительного интервала.
                      Чем больше std, тем шире CI и тем менее чувствительна модель.
        seasonality_period: Период сезонности S. Для почасовых данных с дневной
                           сезонностью S=24, для минутных с часовой — S=60.
        confidence_level: Уровень доверия для CI, по умолчанию 0.95 (95%).

    Returns:
        Словарь с ключами:
        - anomaly: True если значение вне доверительного интервала
        - forecast: Ожидаемое значение
        - lower_ci, upper_ci: Границы доверительного интервала
        - value: Исходное значение (для удобства)
        - message: Человекочитаемое сообщение
    """
    history = list(history)
    n = len(history)

    # При слишком короткой истории не можем построить модель — возвращаем "не аномалия"
    if n < 2:
        return {
            "anomaly": False,
            "forecast": value,
            "lower_ci": value,
            "upper_ci": value,
            "value": value,
            "message": "insufficient history",
        }

    # Последнее и предпоследнее значения — основа для краткосрочного прогноза
    last = history[-1]
    prev = history[-2]

    # AR(1): если последнее значение больше предпоследнего,
    #         это говорит о положительном тренде — корректируем прогноз вверх
    ar_correction = ar_params * (last - prev)

    # MA(1): дополнительное сглаживание через разность
    ma_correction = ma_params * (last - prev)

    # Сезонная коррекция: смотрим на значение S периодов назад.
    # Если текущее значение сильно отклоняется от того, что было S период назад,
    # это может указывать на сезонную аномалию (например, нагрузка в непривычное время).
    seasonal_correction = 0.0
    seasonal_ma_correction = 0.0
    if seasonality_period > 0 and n > seasonality_period:
        seasonal_val = history[-seasonality_period - 1]
        seasonal_correction = seasonal_ar_params * (last - seasonal_val)
        seasonal_ma_correction = seasonal_ma_params * (last - seasonal_val)

    # Итоговый прогноз — базовое значение плюс все коррекции
    forecast = last + ar_correction + ma_correction + seasonal_correction + seasonal_ma_correction

    # Доверительный интервал: при нормальном распределении остатков
    # CI = прогноз ± z × std, где z — квантиль для (1+confidence)/2
    # Для 95% CI: z ≈ 1.96, интервал охватывает 95% вероятной массы
    z = norm_quantile((1 + confidence_level) / 2)
    half_width = z * residual_std
    lower_ci = forecast - half_width
    upper_ci = forecast + half_width

    # Аномалия: значение за пределами доверительного интервала
    is_anomaly = (value < lower_ci or value > upper_ci)

    if is_anomaly:
        msg = f"value={value:.4f} outside CI [{lower_ci:.4f}, {upper_ci:.4f}]"
    else:
        msg = f"value={value:.4f} inside CI [{lower_ci:.4f}, {upper_ci:.4f}]"

    return {
        "anomaly": is_anomaly,
        "forecast": float(forecast),
        "lower_ci": float(lower_ci),
        "upper_ci": float(upper_ci),
        "value": float(value),
        "message": msg,
    }
