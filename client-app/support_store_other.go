//go:build !windows && !linux

package main

import "errors"

// support_store_other.go — off Windows there is no DPAPI: the support
// servers cannot be kept (support.go answers "storage").

type noSupportStore struct{}

func newSupportStore() supportBlobStore { return noSupportStore{} }

func (noSupportStore) Load() ([]byte, error) {
	return nil, errors.New("not available on this platform")
}

func (noSupportStore) Save([]byte) error { return errors.New("not available on this platform") }
