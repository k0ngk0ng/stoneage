// stoneage-assets-sync publishes the browser's immutable static trees to an
// Aliyun OSS bucket. It is intentionally a separate control-plane command:
// the game Web process only serves protocol traffic and public asset URLs,
// while this command is the only component that receives OSS credentials.
package main

import (
	"errors"
	"flag"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

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
	Name string
	Root string
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
	clientRoot := flags.String("client-data", "/game/client", "2.5 client data root containing map/ and data/")
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
	const accessKeyIDEnv = "ALIBABA_CLOUD_ACCESS_KEY_ID"
	const accessKeySecretEnv = "ALIBABA_CLOUD_ACCESS_KEY_SECRET"
	accessKeyID := strings.TrimSpace(os.Getenv(accessKeyIDEnv))
	accessKeySecret := strings.TrimSpace(os.Getenv(accessKeySecretEnv))
	if !*dryRun && (accessKeyID == "" || accessKeySecret == "") {
		return fmt.Errorf("OSS credentials are missing: set %s and %s", accessKeyIDEnv, accessKeySecretEnv)
	}

	assetDirectory := strings.TrimSpace(*assetsRoot)
	if assetDirectory == "" {
		assetDirectory = strings.TrimSpace(disk.Static.AssetsDirectory)
	}
	clientDirectory := strings.TrimSpace(*clientRoot)
	if clientDirectory == "" {
		return errors.New("-client-data must not be empty")
	}
	trees := []sourceTree{
		{Name: "assets", Root: assetDirectory},
		{Name: "maps", Root: filepath.Join(clientDirectory, "map")},
		{Name: "audio", Root: filepath.Join(clientDirectory, "data")},
	}
	for _, tree := range trees {
		if strings.TrimSpace(tree.Root) == "" {
			return fmt.Errorf("source directory for %s is empty", tree.Name)
		}
		info, statErr := os.Stat(tree.Root)
		if statErr != nil {
			return fmt.Errorf("source directory for %s is unavailable: %w", tree.Name, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("source path for %s is not a directory", tree.Name)
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

	count := 0
	for _, tree := range trees {
		uploaded, syncErr := syncTree(bucket, tree, prefix, *dryRun, *workers)
		if syncErr != nil {
			return syncErr
		}
		count += uploaded
	}
	if *dryRun {
		targetBucket := bucketName
		if targetBucket == "" {
			targetBucket = "<bucket-not-configured>"
		}
		fmt.Printf("dry-run: %d objects would be uploaded to oss://%s/%s\n", count, targetBucket, prefix)
	} else {
		fmt.Printf("uploaded %d objects to oss://%s/%s\n", count, bucketName, prefix)
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
		err := filepath.WalkDir(tree.Root, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symbolic link %q", filename)
			}
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
	walkErr := filepath.WalkDir(tree.Root, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symbolic link %q", filename)
		}
		relative, err := filepath.Rel(tree.Root, filename)
		if err != nil {
			return err
		}
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
