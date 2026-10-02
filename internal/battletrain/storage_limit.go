package battletrain

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// StorageLimitError stops collection/optimization after a committed checkpoint.
// It is a logical retained-file threshold, not an OS quota or a cleanup policy.
type StorageLimitError struct {
	Bytes int64
	Limit int64
}

func (e *StorageLimitError) Error() string {
	return fmt.Sprintf("training data reached the stop threshold (%d >= %d bytes); checkpoint retained; resume with a higher --stop-at-data-bytes or 0", e.Bytes, e.Limit)
}

// Count regular-file lengths, including logs and immutable history, without
// following symlinks. Hard links count at each path; sparse-file lengths count
// in full. This intentionally does not claim filesystem allocated-space usage.
func trainingDataBytes(ctx context.Context, root string) (int64, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("training data root is not a directory")
	}
	var total int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || total > math.MaxInt64-info.Size() {
			return fmt.Errorf("training data size overflow")
		}
		total += info.Size()
		return nil
	})
	return total, err
}
