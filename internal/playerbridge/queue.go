// Package playerbridge connects the admin service to the private file queues
// consumed by the legacy main loops. It never opens character save paths.
package playerbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type Queue struct {
	Requests  string
	Responses string
	Timeout   time.Duration
}

func (q Queue) Exchange(ctx context.Context, encode func(string) ([]byte, error)) ([]byte, string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, "", err
	}
	id := hex.EncodeToString(random[:])
	payload, err := encode(id)
	if err != nil {
		return nil, id, err
	}
	if len(payload) > 160<<10 {
		return nil, id, fmt.Errorf("管理请求过大")
	}
	duration := q.Timeout
	if duration <= 0 {
		duration = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return nil, id, err
	}
	// The game process owns directory creation and private permissions. Missing
	// directories mean the bridge is not running/configured, not an invitation
	// to silently create a queue no process will consume.
	tmp, err := os.CreateTemp(q.Requests, ".request-*")
	if err != nil {
		return nil, id, fmt.Errorf("%w: 游戏服务请求目录不可用", playerdata.ErrUnavailable)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(payload); err != nil {
		tmp.Close()
		return nil, id, err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return nil, id, err
	}
	if err = tmp.Close(); err != nil {
		return nil, id, err
	}
	requestPath := filepath.Join(q.Requests, id+".req")
	responsePath := filepath.Join(q.Responses, id+".resp")
	if err = os.Rename(tmpName, requestPath); err != nil {
		return nil, id, err
	}
	defer os.Remove(requestPath)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := readResponse(responsePath)
		if err == nil {
			os.Remove(responsePath)
			return result, id, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, id, fmt.Errorf("%w: 无法读取操作结果", playerdata.ErrUnavailable)
		}
		select {
		case <-ctx.Done():
			// A consumer may already have opened the request. Never automatically
			// retry a mutation after this point; its outcome must be reconciled.
			return nil, id, fmt.Errorf("%w: 操作结果尚未确认，请刷新角色数据后检查", playerdata.ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func readResponse(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return nil, fmt.Errorf("invalid bridge response file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if len(result) > 256<<10 {
		return nil, fmt.Errorf("oversized bridge response")
	}
	return result, err
}
