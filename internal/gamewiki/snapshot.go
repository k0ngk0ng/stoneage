package gamewiki

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StaticIndex contains only lookup text and list summaries. Details live in
// independent shards and never participate in search or stay resident online.
type StaticIndex struct {
	Revision   string     `json:"revision"`
	Categories []category `json:"categories"`
	Notes      []string   `json:"notes"`
	Rows       [][]string `json:"rows,omitempty"`
}

func shardFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:1])
}
func searchText(e *Entry) string {
	parts := []string{e.Key, e.Name, e.Description, e.Location, e.Group, e.Skills}
	for _, f := range e.Fields {
		if strings.Contains(f.Label, "编号") || f.Label == "历史攻略奖励" || f.Label == "历史攻略前提" {
			parts = append(parts, f.Value)
		}
	}
	runes := []rune(strings.ToLower(strings.Join(parts, " ")))
	if len(runes) > 1024 {
		runes = runes[:1024]
	}
	return string(runes)
}
func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		f.Close()
		return err
	}
	encErr := json.NewEncoder(z).Encode(v)
	zErr := z.Close()
	fErr := f.Close()
	if encErr != nil {
		return encErr
	}
	if zErr != nil {
		return zErr
	}
	return fErr
}

// BuildSnapshot runs locally before a release. No runtime calls this builder.
func BuildSnapshot(ctx context.Context, root, output string) error {
	c, err := Load(ctx, root)
	if err != nil {
		return err
	}
	index := StaticIndex{Revision: c.Revision, Categories: append([]category(nil), categories...), Notes: append([]string(nil), c.Notes...)}
	index.Notes[0] = "资料与搜索索引在发布前生成；搜索在浏览器本地完成，游戏表变更后需重新生成并发布。"
	for i := range index.Categories {
		index.Categories[i].Count = c.Counts[index.Categories[i].ID]
	}
	shards := map[string]map[string]*Entry{}
	for _, s := range c.List {
		e := c.Entries[s.Key]
		shard := shardFor(s.Key)
		if shards[shard] == nil {
			shards[shard] = map[string]*Entry{}
		}
		shards[shard][s.Key] = e
		// Fixed-position rows avoid repeating JSON property names 22,000 times.
		index.Rows = append(index.Rows, []string{s.Key, s.Kind, s.Name, s.Description, s.Location, s.Level, s.HP, s.Skills, s.Group, searchText(e), shard})
	}
	// Content-address every shard so cached index and details remain consistent.
	// Hash serialized content as well as the source revision (generator changes matter).
	hash := sha256.New()
	raw, _ := json.Marshal(index)
	hash.Write(raw)
	names := make([]string, 0, len(shards))
	for name := range shards {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, _ := json.Marshal(shards[name])
		hash.Write(raw)
	}
	index.Revision = hex.EncodeToString(hash.Sum(nil))[:16]
	directory := filepath.Join(output, index.Revision)
	if err = os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	for _, name := range names {
		if err = writeJSON(filepath.Join(directory, name+".json.gz"), shards[name]); err != nil {
			return err
		}
	}
	if err = writeJSON(filepath.Join(directory, "index.json.gz"), index); err != nil {
		return err
	}
	for _, category := range index.Categories {
		part := index
		part.Rows = nil
		for _, row := range index.Rows {
			if row[1] == category.ID {
				part.Rows = append(part.Rows, row)
			}
		}
		if err = writeJSON(filepath.Join(directory, "category-"+category.ID+".json.gz"), part); err != nil {
			return err
		}
	}
	index.Rows = nil
	return writeJSON(filepath.Join(output, "catalog.json.gz"), index)
}
