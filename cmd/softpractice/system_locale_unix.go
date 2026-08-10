//go:build !windows

package main

func systemLocaleName() string {
	return ""
}
