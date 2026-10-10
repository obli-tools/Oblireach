//go:build !windows && !linux && !darwin

package main

func readMachineUUID() string { return "" }
