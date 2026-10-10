//go:build !race

package main

// raceBuild: the tests run with -race (much slower process start-up).
const raceBuild = false
