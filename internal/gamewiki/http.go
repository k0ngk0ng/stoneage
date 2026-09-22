package gamewiki

import (
	"compress/gzip"
	"embed"
	"io"
	"net/http"
	"path"
	"strings"
)

//go:embed site/* quests.json snapshot
var content embed.FS
var questData, _ = content.ReadFile("quests.json")

// Handler serves immutable files only. It does not open native game data,
// construct catalogs, search, compress data or keep a catalog cache.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只支持读取静态文件", 405)
		return
	}
	name, mime := "", ""
	switch r.URL.Path {
	case "/wiki", "/wiki/":
		name = "site/index.html"
		mime = "text/html; charset=utf-8"
	case "/wiki/app.js", "/wiki/search-worker.js":
		name = "site/" + path.Base(r.URL.Path)
		mime = "text/javascript; charset=utf-8"
	case "/wiki/style.css":
		name = "site/style.css"
		mime = "text/css; charset=utf-8"
	default:
		if !strings.HasPrefix(r.URL.Path, "/wiki/data/") || strings.Contains(r.URL.Path, "..") || !strings.HasSuffix(r.URL.Path, ".json") {
			http.NotFound(w, r)
			return
		}
		name = "snapshot/" + strings.TrimPrefix(r.URL.Path, "/wiki/data/") + ".gz"
		mime = "application/json; charset=utf-8"
	}
	f, err := content.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-cache")
	if strings.Count(name, "/") == 2 && strings.HasPrefix(name, "snapshot/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	var reader io.Reader = f
	if strings.HasSuffix(name, ".gz") {
		w.Header().Set("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			w.Header().Set("Content-Encoding", "gzip")
		} else {
			z, err := gzip.NewReader(f)
			if err != nil {
				http.Error(w, "静态资料不可用", 500)
				return
			}
			defer z.Close()
			reader = z
		}
	}
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, reader)
	}
}
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if fields[0] == "gzip" {
			for _, p := range fields[1:] {
				if strings.TrimSpace(p) == "q=0" {
					return false
				}
			}
			return true
		}
	}
	return false
}

type category struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var categories = []category{{ID: "guide", Name: "新增与修改指南"}, {ID: "quest", Name: "历史任务"}, {ID: "pet", Name: "宠物与生物"}, {ID: "equipment", Name: "装备"}, {ID: "item", Name: "物品"}, {ID: "skill", Name: "宠物技能"}, {ID: "battle_npc", Name: "战斗 NPC"}, {ID: "enemy", Name: "敌人"}, {ID: "map", Name: "地图"}, {ID: "npc", Name: "城镇与任务 NPC"}, {ID: "encounter", Name: "野外遭遇"}, {ID: "server_task", Name: "本服任务验证"}, {ID: "npc_template", Name: "未放置 NPC 模板"}}
