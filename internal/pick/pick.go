// Package pick selects the first usable value from an ordered set of
// candidates. Several packages independently grew the same "return the first
// non-empty header / string / pointer" helper; this is the one definition they
// all share.
package pick

// First returns the first value that is not the zero value for T, or the zero
// value when every candidate is zero. Candidates are evaluated in order, so
// callers express precedence by argument order.
func First[T comparable](values ...T) T {
	var zero T
	for _, value := range values {
		if value != zero {
			return value
		}
	}
	return zero
}

// FirstFunc returns the first non-zero result of lookup applied to keys, or the
// zero value when every lookup yields zero. It suits keyed sources such as
// http.Header.Get or a map read, where the candidate values are not available
// until the key is resolved.
func FirstFunc[K any, V comparable](lookup func(K) V, keys ...K) V {
	var zero V
	for _, key := range keys {
		if value := lookup(key); value != zero {
			return value
		}
	}
	return zero
}
