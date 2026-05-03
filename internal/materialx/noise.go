package materialx

import "math"

// Classical Ken Perlin "Improved Noise" (2002), 3D variant. Returns
// roughly [-1, 1]. Deterministic given (x, y, z); no internal state.

var permTable = [256]int{
	151, 160, 137, 91, 90, 15, 131, 13, 201, 95, 96, 53, 194, 233, 7, 225,
	140, 36, 103, 30, 69, 142, 8, 99, 37, 240, 21, 10, 23, 190, 6, 148,
	247, 120, 234, 75, 0, 26, 197, 62, 94, 252, 219, 203, 117, 35, 11, 32,
	57, 177, 33, 88, 237, 149, 56, 87, 174, 20, 125, 136, 171, 168, 68, 175,
	74, 165, 71, 134, 139, 48, 27, 166, 77, 146, 158, 231, 83, 111, 229, 122,
	60, 211, 133, 230, 220, 105, 92, 41, 55, 46, 245, 40, 244, 102, 143, 54,
	65, 25, 63, 161, 1, 216, 80, 73, 209, 76, 132, 187, 208, 89, 18, 169,
	200, 196, 135, 130, 116, 188, 159, 86, 164, 100, 109, 198, 173, 186, 3, 64,
	52, 217, 226, 250, 124, 123, 5, 202, 38, 147, 118, 126, 255, 82, 85, 212,
	207, 206, 59, 227, 47, 16, 58, 17, 182, 189, 28, 42, 223, 183, 170, 213,
	119, 248, 152, 2, 44, 154, 163, 70, 221, 153, 101, 155, 167, 43, 172, 9,
	129, 22, 39, 253, 19, 98, 108, 110, 79, 113, 224, 232, 178, 185, 112, 104,
	218, 246, 97, 228, 251, 34, 242, 193, 238, 210, 144, 12, 191, 179, 162, 241,
	81, 51, 145, 235, 249, 14, 239, 107, 49, 192, 214, 31, 181, 199, 106, 157,
	184, 84, 204, 176, 115, 121, 50, 45, 127, 4, 150, 254, 138, 236, 205, 93,
	222, 114, 67, 29, 24, 72, 243, 141, 128, 195, 78, 66, 215, 61, 156, 180,
}

var perm [512]int

func init() {
	for i := range 256 {
		perm[i] = permTable[i]
		perm[i+256] = permTable[i]
	}
}

func fade(t float64) float64 {
	return t * t * t * (t*(t*6-15) + 10)
}

func lerp(t, a, b float64) float64 {
	return a + t*(b-a)
}

func grad3(hash int, x, y, z float64) float64 {
	h := hash & 15
	var u, v float64
	if h < 8 {
		u = x
	} else {
		u = y
	}
	if h < 4 {
		v = y
	} else if h == 12 || h == 14 {
		v = x
	} else {
		v = z
	}
	if h&1 != 0 {
		u = -u
	}
	if h&2 != 0 {
		v = -v
	}
	return u + v
}

// perlinGradientScale3d compensates for the fact that the gradient
// vectors used by grad3 (cube-edge directions) aren't unit-length, so
// raw Perlin output peaks slightly above 1. The constant matches
// MaterialX's mx_gradient_scale3d so single-octave Perlin output
// across our evaluator and the reference GLSL implementation stays in
// the same numeric range.
const perlinGradientScale3d = 0.9820

// perlin3D returns a value in approximately [-1, 1].
func perlin3D(x, y, z float64) float64 {
	fx := math.Floor(x)
	fy := math.Floor(y)
	fz := math.Floor(z)
	X := int(fx) & 255
	Y := int(fy) & 255
	Z := int(fz) & 255
	x -= fx
	y -= fy
	z -= fz
	u := fade(x)
	v := fade(y)
	w := fade(z)
	A := perm[X] + Y
	AA := perm[A] + Z
	AB := perm[A+1] + Z
	B := perm[X+1] + Y
	BA := perm[B] + Z
	BB := perm[B+1] + Z
	r := lerp(w,
		lerp(v,
			lerp(u, grad3(perm[AA], x, y, z), grad3(perm[BA], x-1, y, z)),
			lerp(u, grad3(perm[AB], x, y-1, z), grad3(perm[BB], x-1, y-1, z))),
		lerp(v,
			lerp(u, grad3(perm[AA+1], x, y, z-1), grad3(perm[BA+1], x-1, y, z-1)),
			lerp(u, grad3(perm[AB+1], x, y-1, z-1), grad3(perm[BB+1], x-1, y-1, z-1))))
	return r * perlinGradientScale3d
}

// fractal3D returns a fractal Brownian motion sum of Perlin noises,
// matching the reference MaterialX implementation
// (libraries/stdlib/genglsl/lib/mx_noise.glsl, mx_fractal3d_noise_float):
// raw amplitude-weighted sum without normalization. With diminish=0.5
// and octaves=3 the output ranges roughly [-1.75, 1.75]; the spec
// only commits to "approximately [-1, 1]" but every reference
// implementation we've checked (GLSL/OSL/MDL gen libraries) skips the
// normalize step. Normalizing here makes the noise term in
// downstream graphs (e.g. standard_surface_marble_solid.mtlx, where
// scale_noise = 3 * fractal3d competes with a linear position carrier
// term) under-amplitude relative to the reference, which produces
// visibly straighter bands.
func fractal3D(x, y, z float64, octaves int, lacunarity, diminish float64) float64 {
	if octaves < 1 {
		octaves = 1
	}
	var sum float64
	amp := 1.0
	freq := 1.0
	for i := 0; i < octaves; i++ {
		sum += amp * perlin3D(x*freq, y*freq, z*freq)
		freq *= lacunarity
		amp *= diminish
	}
	return sum
}
