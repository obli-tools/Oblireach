//go:build !go1.21

package engine

// min / max for the "nt6" build line (Windows 7 SP1 / 8.1 / Server 2008 R2 /
// 2012 / 2012 R2), compiled with Go 1.20.14 through go120.mod (see
// BUILD_NT6.txt): the min / max builtins only exist from Go 1.21. Same
// semantics as the builtins for the ordered types the agent uses (at least
// one argument, variadic). Never compiled by Go 1.21+ toolchains, where the
// builtins apply (the modern build: go.mod, go 1.22).

type minMaxOrdered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64 | ~string
}

func min[T minMaxOrdered](x T, ys ...T) T {
	m := x
	for _, y := range ys {
		if y < m {
			m = y
		}
	}
	return m
}

func max[T minMaxOrdered](x T, ys ...T) T {
	m := x
	for _, y := range ys {
		if y > m {
			m = y
		}
	}
	return m
}
