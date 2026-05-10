// Package core provides anomaly detection rule types and the rule engine.
package core

import (
	"math"
	"sort"
)

// KsTest returns the asymptotic p-value for a two-sample Kolmogorov-Smirnov test
// comparing the reference window against the current window.
//
// Returns 1.0 if either slice has fewer than 2 elements — insufficient data
// to reject the null hypothesis (no anomaly).
//
// The two-sided null hypothesis: both samples are drawn from the same distribution.
// If p-value < significance_level, we reject the null hypothesis → anomaly.
func KsTest(reference, current []float64) float64 {
	n, m := len(reference), len(current)
	if n < 2 || m < 2 {
		return 1.0 // cannot reject H0
	}

	// Sort copies so original slices are not mutated.
	refSorted := make([]float64, n)
	copy(refSorted, reference)
	curSorted := make([]float64, m)
	copy(curSorted, current)

	sort.Float64s(refSorted)
	sort.Float64s(curSorted)

	// Compute the maximum vertical distance between the two ECDFs.
	// ECDF at x: number of elements <= x / total.
	d := 0.0
	i, j := 0, 0
	for i < n && j < m {
		xi, xj := refSorted[i], curSorted[j]
		edfX := float64(i+1) / float64(n)
		edfY := float64(j+1) / float64(m)

		diff := math.Abs(edfX - edfY)
		if diff > d {
			d = diff
		}

		if xi < xj {
			i++
		} else if xj < xi {
			j++
		} else {
			// Equal values — advance both to avoid double-counting the jump.
			i++
			j++
		}
	}

	// Asymptotic approximation for the two-sided test.
	// Smirnov's formula: p ≈ 2 * exp(-2 * D² * n*m/(n+m))
	p := 2.0 * math.Exp(-2.0*d*d*float64(n)*float64(m)/float64(n+m))
	if p > 1.0 {
		return 1.0
	}
	if p < 0.0 {
		return 0.0
	}
	return p
}
