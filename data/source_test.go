package data

import (
	"math"
	"testing"
)

func TestRangeOperations(t *testing.T) {
	r1 := Range{Min: -2, Max: 8}
	if !r1.Valid() || r1.Span() != 10 || !r1.Contains(0) || r1.Contains(9) {
		t.Fatalf("unexpected Range behavior: %+v", r1)
	}
	r2 := Range{Min: 5, Max: 15}
	u := r1.Union(r2)
	if u.Min != -2 || u.Max != 15 {
		t.Fatalf("Union = %+v, want [-2, 15]", u)
	}
	deg := Range{Min: 0, Max: 0}.Pad(0.1)
	if deg.Span() <= 0 {
		t.Fatalf("Pad on zero-span range did not expand: %+v", deg)
	}
	nanRange := Range{Min: math.NaN(), Max: 1}
	if nanRange.Valid() {
		t.Fatal("NaN range should be invalid")
	}
}

func TestXYSliceAndBounds(t *testing.T) {
	xs := []float64{0, 1, 2, 3, 4}
	ys := []float64{10, -5, math.NaN(), 25, 3}
	s := NewXYSlice(xs, ys)

	if s.Len() != 5 || !s.MonotonicX() {
		t.Fatalf("Len=%d MonotonicX=%v", s.Len(), s.MonotonicX())
	}
	xb, yb, ok := s.Bounds()
	if !ok || xb != (Range{Min: 0, Max: 4}) || yb != (Range{Min: -5, Max: 25}) {
		t.Fatalf("Bounds = (%+v, %+v, %v)", xb, yb, ok)
	}
	v1 := s.Version()
	s.Append([]float64{5, 4.5}, []float64{100, -20})
	if s.Version() <= v1 {
		t.Fatal("Version did not increment on Append")
	}
	if s.MonotonicX() {
		t.Fatal("expected MonotonicX=false after appending decreasing X")
	}
	xb, yb, ok = s.Bounds()
	if !ok || xb.Max != 5 || yb.Min != -20 || yb.Max != 100 {
		t.Fatalf("updated Bounds = (%+v, %+v, %v)", xb, yb, ok)
	}
}

func TestUniformSliceAndBinarySearch(t *testing.T) {
	ys := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	u := NewUniformSlice(100.0, 10.0, ys) // dt = 0.1, x in [100.0, 100.9]

	xb, yb, ok := u.Bounds()
	if !ok || math.Abs(xb.Min-100.0) > 1e-12 || math.Abs(xb.Max-100.9) > 1e-12 || yb.Min != 1 || yb.Max != 10 {
		t.Fatalf("UniformSlice Bounds = (%+v, %+v, %v)", xb, yb, ok)
	}
	lo := LowerBoundX(u, 100.25)
	hi := UpperBoundX(u, 100.65)
	if lo != 3 || hi != 7 {
		t.Fatalf("LowerBoundX=%d (want 3), UpperBoundX=%d (want 7)", lo, hi)
	}

	// Compare with XYSlice binary search
	xs := make([]float64, len(ys))
	for i := range xs {
		xs[i], _ = u.XY(i)
	}
	xy := NewXYSlice(xs, ys)
	if LowerBoundX(xy, 100.25) != lo || UpperBoundX(xy, 100.65) != hi {
		t.Fatalf("XYSlice binary search mismatch")
	}
}

func TestFuncSource(t *testing.T) {
	fn := NewFuncSource(0, math.Pi, 101, math.Sin)
	xb, yb, ok := fn.Bounds()
	if !ok || xb.Min != 0 || math.Abs(xb.Max-math.Pi) > 1e-12 || math.Abs(yb.Max-1.0) > 1e-3 {
		t.Fatalf("FuncSource Bounds = (%+v, %+v, %v)", xb, yb, ok)
	}
}
