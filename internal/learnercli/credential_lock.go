package learnercli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Keep the lock file in place: deleting it could let another process lock a
// different inode while a previous holder is still using the credentials.
func (s CredentialStore) lock(ctx context.Context) (func(), error) {
	if s.Path == "" {
		return nil, fmt.Errorf("credential metadata path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open credential lock: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		locked, err := tryCredentialLock(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("lock credentials: %w", err)
		}
		if locked {
			return func() { file.Close() }, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
