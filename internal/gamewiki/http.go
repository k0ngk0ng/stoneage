package gamewiki

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed site/* quests.json
var content embed.FS
var questData, _ = content.ReadFile("quests.json")

type Handler struct {
	mu       sync.Mutex
	load     func(context.Context) (*Catalog, error)
	cached   *Catalog
	failedAt time.Time
	failure  error
}

func NewHandler(root string) *Handler {
	return &Handler{load: func(ctx context.Context) (*Catalog, error) { return Load(ctx, root) }}
}
func (h *Handler) catalog(ctx context.Context) (*Catalog, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cached != nil && time.Since(h.cached.LoadedAt) < 5*time.Minute {
		return h.cached, nil
	}
	if h.failure != nil && time.Since(h.failedAt) < 30*time.Second {
		return nil, errors.New("wiki catalog retry delayed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := h.load(ctx)
	if err == nil {
		h.cached = c
		h.failure = nil
	} else {
		h.failure = err
		h.failedAt = time.Now()
	}
	return c, err
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只支持查询", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/wiki/api" {
		h.api(w, r)
		return
	}
	file := ""
	mime := ""
	switch r.URL.Path {
	case "/wiki", "/wiki/":
		file = "index.html"
		mime = "text/html; charset=utf-8"
	case "/wiki/app.js":
		file = "app.js"
		mime = "text/javascript; charset=utf-8"
	case "/wiki/style.css":
		file = "style.css"
		mime = "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	body, err := content.ReadFile("site/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", mime)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

type category struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var categories = []category{{ID: "guide", Name: "新增与修改指南"}, {ID: "quest", Name: "历史任务"}, {ID: "pet", Name: "宠物与生物"}, {ID: "equipment", Name: "装备"}, {ID: "item", Name: "物品"}, {ID: "skill", Name: "宠物技能"}, {ID: "battle_npc", Name: "战斗 NPC"}, {ID: "enemy", Name: "敌人"}, {ID: "map", Name: "地图"}, {ID: "npc", Name: "城镇与任务 NPC"}, {ID: "encounter", Name: "野外遭遇"}, {ID: "server_task", Name: "本服任务验证"}, {ID: "npc_template", Name: "未放置 NPC 模板"}}

func (h *Handler) api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	c, err := h.catalog(r.Context())
	if err != nil {
		log.Printf("wiki data unavailable: %v", err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "游戏资料暂时无法读取，请稍后重试"})
		return
	}
	if id := r.URL.Query().Get("entry"); id != "" {
		if e := c.Entries[id]; e != nil {
			_ = json.NewEncoder(w).Encode(e)
		} else {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "没有找到该条目"})
		}
		return
	}
	kind, q, group := r.URL.Query().Get("kind"), strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))), r.URL.Query().Get("group")
	if len(q) > 256 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "搜索内容过长"})
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}
	cs := append([]category(nil), categories...)
	for i := range cs {
		cs[i].Count = c.Counts[cs[i].ID]
	}
	rows := make([]Summary, 0, limit)
	total := 0
	for _, s := range c.List {
		if kind != "" && kind != s.Kind || group != "" && group != s.Group {
			continue
		}
		e := c.Entries[s.Key]
		if q != "" && !strings.Contains(e.search, q) {
			continue
		}
		if total >= offset && len(rows) < limit {
			rows = append(rows, s)
		}
		total++
	}
	_ = json.NewEncoder(w).Encode(struct {
		Entries    []Summary  `json:"entries"`
		Categories []category `json:"categories"`
		Total      int        `json:"total"`
		Offset     int        `json:"offset"`
		Limit      int        `json:"limit"`
		Revision   string     `json:"revision"`
		LoadedAt   time.Time  `json:"loaded_at"`
		Notes      []string   `json:"notes"`
	}{rows, cs, total, offset, limit, c.Revision, c.LoadedAt, c.Notes})
}
