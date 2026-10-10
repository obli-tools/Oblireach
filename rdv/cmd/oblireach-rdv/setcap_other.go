//go:build !linux

package main

import "errors"

func setcapSelf() error { return errors.New("setcap-self: Linux only") }

func raiseBindCap() bool { return false }
