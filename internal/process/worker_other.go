//go:build !linux

package process

func RunWorkerIfRequested() bool { return false }
