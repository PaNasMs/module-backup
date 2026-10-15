package backup

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

func exclusiveMaintenance(ctx context.Context) (func(), error) {
	path := os.Getenv("PANASMS_MAINTENANCE_LOCK")
	if path == "" {
		return func() {}, nil
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { file.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			file.Close()
			return nil, errors.New("Other NAS tasks are active; retry when they finish")
		case <-tick.C:
		}
	}
}
