package voxel

import (
	"math"
	"testing"

	sparse "github.com/james-bowman/sparse"
	"gonum.org/v1/gonum/mat"
)

// Sanity check: solve a tiny SPD system A x = b via james-bowman/sparse
// Cholesky, comparing against the known solution.
//
//	A = [ 4 1 0 ]    b = [ 5 ]    expected x = [ 1, 1, 1 ]
//	    [ 1 3 1 ]        [ 5 ]
//	    [ 0 1 2 ]        [ 3 ]
func TestSparseCholeskySmoke(t *testing.T) {
	dok := sparse.NewDOK(3, 3)
	dok.Set(0, 0, 4)
	dok.Set(0, 1, 1)
	dok.Set(1, 0, 1)
	dok.Set(1, 1, 3)
	dok.Set(1, 2, 1)
	dok.Set(2, 1, 1)
	dok.Set(2, 2, 2)
	csr := dok.ToCSR()

	var ch sparse.Cholesky
	ch.Factorize(csr)

	b := mat.NewVecDense(3, []float64{5, 5, 3})
	x := mat.NewVecDense(3, nil)
	if err := ch.SolveVecTo(x, b); err != nil {
		t.Fatalf("SolveVecTo: %v", err)
	}

	want := []float64{1, 1, 1}
	for i := 0; i < 3; i++ {
		got := x.AtVec(i)
		if math.Abs(got-want[i]) > 1e-9 {
			t.Errorf("x[%d] = %g; want %g", i, got, want[i])
		}
	}
}
