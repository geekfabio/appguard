//go:build !windows

package singleton

// Acquire is a no-op off Windows.
func Acquire(name string) (func(), error) {
	return func() {}, nil
}
