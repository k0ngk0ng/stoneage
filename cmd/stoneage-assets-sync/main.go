// stoneage-assets-sync publishes the browser's immutable static trees to an
// Aliyun OSS bucket. It is intentionally a separate control-plane command:
// the game Web process only serves protocol traffic and public asset URLs,
// while this command is the only component that receives OSS credentials.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/pelletier/go-toml/v2"
)

type configFile struct {
	ListenAddress string           `toml:"listen_address"`
	TCPUpstream   string           `toml:"tcp_upstream"`
	PacketLimit   int              `toml:"packet_limit"`
	MaxSessions   int              `toml:"max_sessions"`
	PollTimeout   string           `toml:"poll_timeout"`
	IdleTimeout   string           `toml:"idle_timeout"`
	DialTimeout   string           `toml:"dial_timeout"`
	AllowedOrigin string           `toml:"allowed_origin"`
	Static        staticConfigFile `toml:"static"`
}

type staticConfigFile struct {
	AssetsDirectory string        `toml:"assets_directory"`
	MapsDirectory   string        `toml:"maps_directory"`
	AudioDirectory  string        `toml:"audio_directory"`
	NPCDirectory    string        `toml:"npc_directory"`
	OSS             ossConfigFile `toml:"oss"`
	CDN             cdnConfigFile `toml:"cdn"`
}

type ossConfigFile struct {
	Provider string `toml:"provider"`
	Endpoint string `toml:"endpoint"`
	Region   string `toml:"region"`
	Bucket   string `toml:"bucket"`
	Prefix   string `toml:"prefix"`
}

type cdnConfigFile struct {
	BaseURL string `toml:"base_url"`
}

type sourceTree struct {
	Name  string
	Roots []sourceRoot
}

// sourceRoot describes one public subtree (or file) and the path it gets
// below the stable client root.  Keeping this list explicit is intentional:
// runtime/legacy-client/data also contains savedata, chat history and client
// binaries which must never be copied to a public bucket.
type sourceRoot struct {
	Path   string
	Prefix string
}

// clientManifestName deliberately lives beside (rather than inside) the three
// public trees.  It is a publication marker for the uploader and is never
// requested by the browser.  A manifest is written only after every changed
// object has uploaded successfully, so a retry can safely reuse the last
// complete publication and skip unchanged files.
const clientManifestName = "_client-manifest.json"

type plannedObject struct {
	Filename string
	Key      string
	Size     int64
	SHA256   string
}

type manifestObject struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type clientManifest struct {
	Format    int                       `json:"format"`
	Generated string                    `json:"generated_at"`
	Objects   map[string]manifestObject `json:"objects"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "stoneage-assets-sync: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("stoneage-assets-sync", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", envOr("STONEAGE_WEB_CONFIG", "/etc/stoneage/web.toml"), "Web TOML configuration")
	assetsRoot := flags.String("assets", "", "sprite asset directory (defaults to static.assets_directory)")
	clientRoot := flags.String("client-data", "/game/client", "2.5 client root containing map/ and data/{auto.dat,bgm,se}")
	dryRun := flags.Bool("dry-run", false, "list the upload plan without writing OSS objects")
	workers := flags.Int("workers", envPositiveInt("STONEAGE_ASSET_SYNC_WORKERS", 8), "parallel OSS uploads")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *workers < 1 || *workers > 64 {
		return errors.New("-workers must be between 1 and 64")
	}

	disk, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(disk.Static.OSS.Provider)) != "" && strings.ToLower(strings.TrimSpace(disk.Static.OSS.Provider)) != "aliyun-oss" {
		return fmt.Errorf("unsupported OSS provider %q", disk.Static.OSS.Provider)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(disk.Static.OSS.Endpoint), "/")
	bucketName := strings.TrimSpace(disk.Static.OSS.Bucket)
	if !*dryRun && (endpoint == "" || bucketName == "") {
		return errors.New("static.oss.endpoint and static.oss.bucket must be configured before syncing")
	}
	if !*dryRun && strings.TrimSpace(disk.Static.OSS.Region) == "" {
		return errors.New("static.oss.region must be configured for OSS V4 uploads")
	}
	prefix := strings.Trim(strings.TrimSpace(disk.Static.OSS.Prefix), "/")
	if err := validateObjectPrefix(prefix); err != nil {
		return err
	}
	const accessKeyIDEnv = "ALIBABA_CLOUD_ACCESS_KEY_ID"
	const accessKeySecretEnv = "ALIBABA_CLOUD_ACCESS_KEY_SECRET"
	// Prefer file-mounted credentials (Docker secrets, Kubernetes secrets, or
	// a CI secret file). Environment variables remain a deliberate fallback so
	// the binary can still be used directly by existing CI jobs. The file form
	// keeps AK/SK out of `docker inspect`, Compose's rendered environment and
	// the long-running web/admin processes.
	accessKeyID, accessKeySecret := "", ""
	if !*dryRun {
		accessKeyID, err = credentialValue(accessKeyIDEnv, "ALIBABA_CLOUD_ACCESS_KEY_ID_FILE")
		if err != nil {
			return err
		}
		accessKeySecret, err = credentialValue(accessKeySecretEnv, "ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE")
		if err != nil {
			return err
		}
	}
	if !*dryRun && (accessKeyID == "" || accessKeySecret == "") {
		return fmt.Errorf("OSS credentials are missing: set %s/%s files or %s and %s", "ALIBABA_CLOUD_ACCESS_KEY_ID_FILE", "ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE", accessKeyIDEnv, accessKeySecretEnv)
	}

	assetDirectory := strings.TrimSpace(*assetsRoot)
	if assetDirectory == "" {
		assetDirectory = strings.TrimSpace(disk.Static.AssetsDirectory)
	}
	clientDirectory := strings.TrimSpace(*clientRoot)
	if clientDirectory == "" {
		return errors.New("-client-data must not be empty")
	}
	dataDirectory := filepath.Join(clientDirectory, "data")
	// Do not use dataDirectory itself as the audio root.  It contains private
	// files (savedata.dat, chat history, PE support data) in addition to the
	// browser's public audio/map colour resources.
	trees := []sourceTree{
		{Name: "assets", Roots: []sourceRoot{{Path: assetDirectory}}},
		{Name: "maps", Roots: []sourceRoot{{Path: filepath.Join(clientDirectory, "map")}}},
		{Name: "audio", Roots: []sourceRoot{
			{Path: filepath.Join(dataDirectory, "auto.dat")},
			{Path: filepath.Join(dataDirectory, "bgm"), Prefix: "bgm"},
			{Path: filepath.Join(dataDirectory, "se"), Prefix: "se"},
			// Palet_*.sap is consumed by the browser's automatic-map colour
			// decoder through /audio/pal/. Keep it in the same public tree;
			// omitting it makes CDN-backed maps lose their palette.
			{Path: filepath.Join(dataDirectory, "pal"), Prefix: "pal"},
		}},
	}
	for _, tree := range trees {
		for _, root := range tree.Roots {
			if strings.TrimSpace(root.Path) == "" {
				return fmt.Errorf("source path for %s is empty", tree.Name)
			}
			info, statErr := os.Lstat(root.Path)
			if statErr != nil {
				return fmt.Errorf("source path for %s is unavailable: %w", tree.Name, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symbolic link source for %s: %q", tree.Name, root.Path)
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("source path for %s is not a regular file or directory", tree.Name)
			}
		}
	}

	var bucket *oss.Bucket
	if !*dryRun {
		options := []oss.ClientOption{oss.Timeout(10, 300), oss.AuthVersion(oss.AuthV4)}
		if region := strings.TrimSpace(disk.Static.OSS.Region); region != "" {
			options = append(options, oss.Region(region))
		}
		client, clientErr := oss.New(endpoint, accessKeyID, accessKeySecret, options...)
		if clientErr != nil {
			return fmt.Errorf("create OSS client: %w", clientErr)
		}
		bucket, err = client.Bucket(bucketName)
		if err != nil {
			return fmt.Errorf("open OSS bucket %q: %w", bucketName, err)
		}
	}

	if *dryRun {
		count := 0
		for _, tree := range trees {
			uploaded, syncErr := syncTree(nil, tree, prefix, true, *workers)
			if syncErr != nil {
				return syncErr
			}
			count += uploaded
		}
		targetBucket := bucketName
		if targetBucket == "" {
			targetBucket = "<bucket-not-configured>"
		}
		fmt.Printf("dry-run: %d objects would be uploaded to oss://%s/%s\n", count, targetBucket, prefix)
		return nil
	}

	previous, err := readManifest(bucket, path.Join(prefix, clientManifestName))
	if err != nil {
		return err
	}
	objects, err := buildPlan(trees, prefix)
	if err != nil {
		return err
	}
	uploaded, skipped, err := syncObjects(bucket, objects, previous.Objects, *workers)
	if err != nil {
		return err
	}
	manifest := clientManifest{
		Format:    1,
		Generated: time.Now().UTC().Format(time.RFC3339),
		Objects:   make(map[string]manifestObject, len(objects)),
	}
	for _, object := range objects {
		manifest.Objects[object.Key] = manifestObject{Size: object.Size, SHA256: object.SHA256}
	}
	if err := writeManifest(bucket, path.Join(prefix, clientManifestName), manifest); err != nil {
		return err
	}
	fmt.Printf("uploaded %d objects, skipped %d unchanged objects (total %d) to oss://%s/%s; manifest=%s\n", uploaded, skipped, len(objects), bucketName, prefix, path.Join(prefix, clientManifestName))
	return nil
}

// credentialValue reads one credential from a secret file when configured,
// otherwise from its legacy environment variable. Secret files contain one
// value and may end with a newline (as Docker/Kubernetes secrets commonly do).
func credentialValue(valueEnv, fileEnv string) (string, error) {
	if filename := strings.TrimSpace(os.Getenv(fileEnv)); filename != "" {
		content, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read OSS credential file %s: %w", fileEnv, err)
		}
		value := strings.TrimSpace(string(content))
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("OSS credential file %s must contain one value", fileEnv)
		}
		return value, nil
	}
	return strings.TrimSpace(os.Getenv(valueEnv)), nil
}

// buildPlan walks the explicit client allow-list once and hashes each file.
// Hashes are calculated before any upload starts, so the manifest is stable for
// this invocation. If a source file is edited while a deployment is in
// progress, the next invocation recomputes its hash and uploads it again.
func buildPlan(trees []sourceTree, prefix string) ([]plannedObject, error) {
	objects := make([]plannedObject, 0)
	seen := make(map[string]struct{})
	for _, tree := range trees {
		err := walkTree(tree, func(filename, relative string) error {
			key := path.Join(prefix, tree.Name, filepath.ToSlash(relative))
			if key == "." || strings.HasPrefix(key, "../") {
				return fmt.Errorf("unsafe object key for %q", filename)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			info, err := os.Stat(filename)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("source path is not a regular file: %q", filename)
			}
			digest, size, err := fileDigest(filename)
			if err != nil {
				return fmt.Errorf("hash %s: %w", filename, err)
			}
			seen[key] = struct{}{}
			objects = append(objects, plannedObject{Filename: filename, Key: key, Size: size, SHA256: digest})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("plan %s: %w", tree.Name, err)
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	return objects, nil
}

func fileDigest(filename string) (string, int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func readManifest(bucket *oss.Bucket, key string) (clientManifest, error) {
	reader, err := bucket.GetObject(key)
	if err != nil {
		var serviceErr oss.ServiceError
		if errors.As(err, &serviceErr) && serviceErr.StatusCode == 404 {
			return clientManifest{Format: 1, Objects: make(map[string]manifestObject)}, nil
		}
		return clientManifest{}, fmt.Errorf("read existing client manifest: %w", err)
	}
	defer reader.Close()
	var manifest clientManifest
	if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
		return clientManifest{}, fmt.Errorf("decode existing client manifest: %w", err)
	}
	if manifest.Format != 1 {
		return clientManifest{}, fmt.Errorf("unsupported client manifest format %d", manifest.Format)
	}
	if manifest.Objects == nil {
		manifest.Objects = make(map[string]manifestObject)
	}
	return manifest, nil
}

func syncObjects(bucket *oss.Bucket, objects []plannedObject, previous map[string]manifestObject, workers int) (uploaded, skipped int, err error) {
	type uploadJob struct{ object plannedObject }
	jobs := make(chan uploadJob, workers*2)
	done := make(chan struct{})
	var stopOnce sync.Once
	var firstErr error
	var errMu sync.Mutex
	var uploadedCount atomic.Int64
	var skippedCount atomic.Int64
	setError := func(value error) {
		if value == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = value
			stopOnce.Do(func() { close(done) })
		}
		errMu.Unlock()
	}
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-done:
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					object := job.object
					if old, ok := previous[object.Key]; ok && old.Size == object.Size && old.SHA256 == object.SHA256 {
						skippedCount.Add(1)
						continue
					}
					contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(object.Filename)))
					if contentType == "" {
						contentType = "application/octet-stream"
					}
					cacheControl := "public, max-age=3600, must-revalidate"
					if strings.EqualFold(filepath.Ext(object.Filename), ".json") {
						cacheControl = "no-cache"
					}
					if err := bucket.PutObjectFromFile(object.Key, object.Filename, oss.ContentType(contentType), oss.CacheControl(cacheControl), oss.Meta("sha256", object.SHA256)); err != nil {
						setError(fmt.Errorf("upload %s: %w", object.Key, err))
						return
					}
					uploadedCount.Add(1)
				}
			}
		}()
	}
	send:
	for _, object := range objects {
		select {
		case <-done:
			break send
		case jobs <- uploadJob{object: object}:
		}
	}
	close(jobs)
	group.Wait()
	errMu.Lock()
	err = firstErr
	errMu.Unlock()
	if err != nil {
		return int(uploadedCount.Load()), int(skippedCount.Load()), fmt.Errorf("sync client objects: %w", err)
	}
	return int(uploadedCount.Load()), int(skippedCount.Load()), nil
}

func writeManifest(bucket *oss.Bucket, key string, manifest clientManifest) error {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode client manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := bucket.PutObject(key, bytes.NewReader(payload), oss.ContentType("application/json"), oss.CacheControl("no-cache")); err != nil {
		return fmt.Errorf("upload client manifest %s: %w", key, err)
	}
	return nil
}

func loadConfig(filename string) (configFile, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return configFile{}, fmt.Errorf("read TOML config %q: %w", filename, err)
	}
	var disk configFile
	decoder := toml.NewDecoder(strings.NewReader(string(content))).DisallowUnknownFields()
	if err := decoder.Decode(&disk); err != nil {
		return configFile{}, fmt.Errorf("decode TOML config %q: %w", filename, err)
	}
	return disk, nil
}

func syncTree(bucket *oss.Bucket, tree sourceTree, prefix string, dryRun bool, workers int) (int, error) {
	if dryRun {
		count := 0
		err := walkTree(tree, func(_, _ string) error {
			count++
			return nil
		})
		if err != nil {
			return count, fmt.Errorf("sync %s: %w", tree.Name, err)
		}
		return count, nil
	}

	type uploadJob struct {
		filename string
		key      string
	}
	jobs := make(chan uploadJob, workers*2)
	done := make(chan struct{})
	var stopOnce sync.Once
	var firstErr error
	var errMu sync.Mutex
	var count atomic.Int64
	setError := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			stopOnce.Do(func() { close(done) })
		}
		errMu.Unlock()
	}
	var workerGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for {
				select {
				case <-done:
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(job.filename)))
					if contentType == "" {
						contentType = "application/octet-stream"
					}
					cacheControl := "public, max-age=3600, must-revalidate"
					if strings.EqualFold(filepath.Ext(job.filename), ".json") {
						cacheControl = "no-cache"
					}
					if err := bucket.PutObjectFromFile(job.key, job.filename, oss.ContentType(contentType), oss.CacheControl(cacheControl)); err != nil {
						setError(fmt.Errorf("upload %s: %w", job.key, err))
						return
					}
					count.Add(1)
				}
			}
		}()
	}
	walkErr := walkTree(tree, func(filename, relative string) error {
		key := path.Join(prefix, tree.Name, filepath.ToSlash(relative))
		if key == "." || strings.HasPrefix(key, "../") {
			return fmt.Errorf("unsafe object key for %q", filename)
		}
		select {
		case <-done:
			return errors.New("upload aborted after an earlier error")
		case jobs <- uploadJob{filename: filename, key: key}:
		}
		return nil
	})
	close(jobs)
	workerGroup.Wait()
	if walkErr != nil {
		setError(walkErr)
	}
	errMu.Lock()
	err := firstErr
	errMu.Unlock()
	if err != nil {
		return int(count.Load()), fmt.Errorf("sync %s: %w", tree.Name, err)
	}
	return int(count.Load()), nil
}

// walkTree visits files in a source tree without ever following a symlink.
// The callback receives the source filename and its POSIX-style path below
// the tree's public directory.  A single-file root is supported for
// data/auto.dat, while directory roots retain their complete relative path.
func walkTree(tree sourceTree, visit func(filename, relative string) error) error {
	for _, root := range tree.Roots {
		info, err := os.Lstat(root.Path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symbolic link %q", root.Path)
		}
		if !info.IsDir() {
			relative := filepath.Join(root.Prefix, filepath.Base(root.Path))
			if err := visit(root.Path, relative); err != nil {
				return err
			}
			continue
		}
		err = filepath.WalkDir(root.Path, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symbolic link %q", filename)
			}
			relative, err := filepath.Rel(root.Path, filename)
			if err != nil {
				return err
			}
			relative = filepath.Join(root.Prefix, relative)
			return visit(filename, relative)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func validateObjectPrefix(prefix string) error {
	if strings.ContainsRune(prefix, '\x00') || strings.ContainsRune(prefix, '\\') {
		return errors.New("static.oss.prefix contains an unsafe character")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "." || segment == ".." {
			return errors.New("static.oss.prefix must not contain dot path segments")
		}
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envPositiveInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
