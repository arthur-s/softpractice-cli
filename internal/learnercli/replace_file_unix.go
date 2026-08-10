//go:build !windows

package learnercli

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
