// Package core provides anomaly detection rule types and the rule engine.
package core

import (
	"math"
	"sort"
)

func KsTest(reference, current []float64) float64 {
	n, m := len(reference), len(current)
	if n < 2 || m < 2 {
		return 1.0
	}

	refSorted := append([]float64(nil), reference...)
	curSorted := append([]float64(nil), current...)

	sort.Float64s(refSorted)
	sort.Float64s(curSorted)

	i, j := 0, 0
	f1, f2 := 0.0, 0.0
	d := 0.0

	for i < n && j < m {
		if refSorted[i] < curSorted[j] {
			i++
			f1 = float64(i) / float64(n)
		} else if curSorted[j] < refSorted[i] {
			j++
			f2 = float64(j) / float64(m)
		} else {
			i++
			j++

			f1 = float64(i) / float64(n)
			f2 = float64(j) / float64(m)
		}

		diff := math.Abs(f1 - f2)
		if diff > d {
			d = diff
		}
	}

	for i < n {
		i++
		f1 = float64(i) / float64(n)

		diff := math.Abs(f1 - f2)
		if diff > d {
			d = diff
		}
	}

	for j < m {
		j++
		f2 = float64(j) / float64(m)

		diff := math.Abs(f1 - f2)
		if diff > d {
			d = diff
		}
	}

	// Идентичные распределения
	if d < 1e-15 {
		return 1.0
	}

	lambda := d * math.Sqrt(float64(n*m) / float64(n+m))

	p := 0.0
	for k := 1; k <= 100; k++ {
		term := 2.0 * math.Exp(-2.0*float64(k*k)*lambda*lambda)

		if k%2 == 1 {
			p += term
		} else {
			p -= term
		}

		if term < 1e-12 {
			break
		}
	}

	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}

	return p
}
