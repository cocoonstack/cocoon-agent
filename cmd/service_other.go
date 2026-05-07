//go:build !windows

package cmd

func runAsWindowsService() (bool, error) { return false, nil }
