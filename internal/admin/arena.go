package admin

import (
	"context"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"net/http"
	"strconv"
	"time"
)

type ArenaReader interface {
	ArenaStatus(context.Context, int) (ladder.AdminSnapshot, error)
}

func (server *Server) arenaPage(w http.ResponseWriter, r *http.Request, data *pageData) {
	if r.Method != http.MethodGet {
		playerJSONError(w, 405, "请求方式不支持")
		return
	}
	if r.URL.Path == "/arena" {
		data.Title = "竞技场实时状态"
		server.render(w, "arena", data)
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 256 || n%8 != 0 {
			playerJSONError(w, 400, "分页参数无效")
			return
		}
		offset = n
	}
	if server.arena == nil {
		playerJSONError(w, 503, "竞技场状态服务尚未配置")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	snapshot, err := server.arena.ArenaStatus(ctx, offset)
	if err != nil {
		playerJSONError(w, 503, "暂时无法读取竞技场实时状态，请检查游戏服务")
		return
	}
	playerJSON(w, 200, snapshot)
}
