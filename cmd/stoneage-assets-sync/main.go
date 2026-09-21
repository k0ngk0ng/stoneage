// stoneage-assets-sync publishes the browser's immutable static trees to an
// Aliyun OSS or Cloudflare R2 bucket. It is intentionally a separate
// control-plane command:
// the game Web process only serves protocol traffic and public asset URLs,
// while this command is the only component that receives storage credentials.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	awsV2 "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awsS3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pelletier/go-toml/v2"
)

type configFile struct {
	GatewayAPIURL string           `toml:"gateway_api_url"`
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
// public trees.  It is a publication marker for the uploader and contains the
// per-object hash/size data used for audits and incremental syncs.  A manifest
// is written only after every changed object has uploaded successfully, so a
// retry can safely reuse the last complete publication and skip unchanged
// files.
const clientManifestName = "_client-manifest.json"

// clientVersionName is the small browser-facing companion to the full
// publication manifest.  Loading the complete object map (which can contain
// hundreds of thousands of entries) just to decide whether a Service Worker
// cache is stale would defeat the purpose of the local cache.  The version
// file is published last and contains only the aggregate revision and totals.
const clientVersionName = "_client-version.json"

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

type clientVersion struct {
	Format      int    `json:"format"`
	Generated   string `json:"generated_at"`
	Revision    string `json:"revision"`
	ObjectCount int    `json:"object_count"`
	TotalBytes  int64  `json:"total_bytes"`
	/* DeltaFrom identifies the exact publication manifest against which the
	   changed/removed lists were calculated.  A browser may have skipped one
	   or more releases; without this anchor it could reuse an entry from an
	   older cache that changed in an intermediate release and later reverted. */
	DeltaFrom string `json:"delta_from,omitempty"`
	/* DeltaKnown tells the Service Worker that changed_objects and
	   removed_objects describe this exact revision transition.  Older marker
	   files omit it, so a worker must not reuse an unknown old cache. */
	DeltaKnown     bool     `json:"delta_known,omitempty"`
	ChangedObjects []string `json:"changed_objects,omitempty"`
	RemovedObjects []string `json:"removed_objects,omitempty"`
	ChangedAll     bool     `json:"changed_all,omitempty"`
}

const clientVersionDeltaMax = 8192

// Uploads can encounter a stale keep-alive connection or a short-lived
// provider throttling response. Keep retries bounded so a bad credential,
// missing source file, or other permanent error fails promptly.
const maxUploadAttempts = 4

var uploadRetrySleep = time.Sleep

// objectMetadata is the small provider-neutral subset needed by the browser
// client.  Both Aliyun OSS and Cloudflare R2 preserve these HTTP headers and
// the sha256 metadata used to audit a publication.
type objectMetadata struct {
	ContentType  string
	CacheControl string
	SHA256       string
}

var errObjectNotFound = errors.New("object not found")

var storageBucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// objectStore hides the two S3-like APIs from the publication planner.  The
// web and admin processes never construct one; only this short-lived command
// receives credentials and writes public client objects.
type objectStore interface {
	GetObject(key string) (io.ReadCloser, error)
	PutFile(key, filename string, metadata objectMetadata) error
	Put(key string, payload []byte, metadata objectMetadata) error
}

type aliyunObjectStore struct{ bucket *oss.Bucket }

func (store aliyunObjectStore) GetObject(key string) (io.ReadCloser, error) {
	reader, err := store.bucket.GetObject(key)
	if err != nil {
		var serviceErr oss.ServiceError
		if errors.As(err, &serviceErr) && serviceErr.StatusCode == 404 {
			return nil, errObjectNotFound
		}
		return nil, err
	}
	return reader, nil
}

func aliyunOptions(metadata objectMetadata) []oss.Option {
	options := []oss.Option{oss.ContentType(metadata.ContentType), oss.CacheControl(metadata.CacheControl)}
	if metadata.SHA256 != "" {
		options = append(options, oss.Meta("sha256", metadata.SHA256))
	}
	return options
}

func (store aliyunObjectStore) PutFile(key, filename string, metadata objectMetadata) error {
	return store.bucket.PutObjectFromFile(key, filename, aliyunOptions(metadata)...)
}

func (store aliyunObjectStore) Put(key string, payload []byte, metadata objectMetadata) error {
	return store.bucket.PutObject(key, bytes.NewReader(payload), aliyunOptions(metadata)...)
}

type r2ObjectStore struct {
	client *awsS3.Client
	bucket string
}

func (store r2ObjectStore) GetObject(key string) (io.ReadCloser, error) {
	output, err := store.client.GetObject(context.Background(), &awsS3.GetObjectInput{Bucket: awsV2.String(store.bucket), Key: awsV2.String(key)})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		var responseErr *smithyhttp.ResponseError
		if errors.As(err, &noSuchKey) || (errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == 404) {
			return nil, errObjectNotFound
		}
		return nil, err
	}
	return output.Body, nil
}

func (store r2ObjectStore) PutFile(key, filename string, metadata objectMetadata) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	input := &awsS3.PutObjectInput{
		Bucket:        awsV2.String(store.bucket),
		Key:           awsV2.String(key),
		Body:          file,
		ContentType:   awsV2.String(metadata.ContentType),
		CacheControl:  awsV2.String(metadata.CacheControl),
		ContentLength: awsV2.Int64(info.Size()),
	}
	if metadata.SHA256 != "" {
		input.Metadata = map[string]string{"sha256": metadata.SHA256}
	}
	_, err = store.client.PutObject(context.Background(), input)
	return err
}

func (store r2ObjectStore) Put(key string, payload []byte, metadata objectMetadata) error {
	input := &awsS3.PutObjectInput{
		Bucket:        awsV2.String(store.bucket),
		Key:           awsV2.String(key),
		Body:          bytes.NewReader(payload),
		ContentLength: awsV2.Int64(int64(len(payload))),
		ContentType:   awsV2.String(metadata.ContentType),
		CacheControl:  awsV2.String(metadata.CacheControl),
	}
	if metadata.SHA256 != "" {
		input.Metadata = map[string]string{"sha256": metadata.SHA256}
	}
	_, err := store.client.PutObject(context.Background(), input)
	return err
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
	webOnly := flags.Bool("web-only", false, "publish CDN runtime and compressed indexes without changing core assets")
	mapPacks := flags.String("map-packs", "", "directory with matching map packs for CDN publication")
	dryRun := flags.Bool("dry-run", false, "list the upload plan without writing object-storage objects")
	workers := flags.Int("workers", envPositiveInt("STONEAGE_ASSET_SYNC_WORKERS", 8), "parallel object-storage uploads")
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
	provider, err := normalizeStorageProvider(disk.Static.OSS.Provider)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(disk.Static.OSS.Endpoint), "/")
	bucketName := strings.TrimSpace(disk.Static.OSS.Bucket)
	if !*dryRun && (endpoint == "" || bucketName == "") {
		return fmt.Errorf("static.oss.endpoint and static.oss.bucket must be configured before syncing (%s)", provider)
	}
	if endpoint != "" || bucketName != "" {
		if err := validateStorageEndpoint(endpoint, bucketName); err != nil {
			return err
		}
	}
	region := strings.TrimSpace(disk.Static.OSS.Region)
	if provider == "cloudflare-r2" && region == "" {
		// R2's S3 API uses the literal region "auto".  Keep it as a
		// convenient default while still allowing a future compatible
		// endpoint to provide an explicit region.
		region = "auto"
	}
	if !*dryRun && region == "" {
		return errors.New("static.oss.region must be configured for Aliyun OSS V4 uploads")
	}
	prefix := strings.Trim(strings.TrimSpace(disk.Static.OSS.Prefix), "/")
	if err := validateObjectPrefix(prefix); err != nil {
		return err
	}
	// Prefer file-mounted credentials (Docker secrets, Kubernetes secrets, or
	// a CI secret file). Environment variables remain a deliberate fallback so
	// the binary can still be used directly by existing CI jobs. The file form
	// keeps AK/SK out of `docker inspect`, Compose's rendered environment and
	// the long-running web/admin processes.
	accessKeyID, accessKeySecret := "", ""
	if !*dryRun {
		accessKeyID, err = credentialValueAny(
			[]string{"STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE", "CLOUDFLARE_R2_ACCESS_KEY_ID_FILE", "AWS_ACCESS_KEY_ID_FILE", "ALIBABA_CLOUD_ACCESS_KEY_ID_FILE"},
			[]string{"STONEAGE_ASSET_SYNC_ACCESS_KEY", "CLOUDFLARE_R2_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID"},
		)
		if err != nil {
			return err
		}
		accessKeySecret, err = credentialValueAny(
			[]string{"STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE", "CLOUDFLARE_R2_SECRET_ACCESS_KEY_FILE", "AWS_SECRET_ACCESS_KEY_FILE", "ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE"},
			[]string{"STONEAGE_ASSET_SYNC_ACCESS_SECRET", "CLOUDFLARE_R2_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "ALIBABA_CLOUD_ACCESS_KEY_SECRET"},
		)
		if err != nil {
			return err
		}
	}
	if !*dryRun && (accessKeyID == "" || accessKeySecret == "") {
		return errors.New("object-storage credentials are missing: set STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE and STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE (or provider-compatible environment variables)")
	}

	assetDirectory := strings.TrimSpace(*assetsRoot)
	if assetDirectory == "" {
		assetDirectory = strings.TrimSpace(disk.Static.AssetsDirectory)
	}
	if *webOnly {
		if *dryRun {
			return errors.New("web-only requires reading the published manifest; dry-run is not supported")
		}
		store, err := newObjectStore(provider, endpoint, region, bucketName, accessKeyID, accessKeySecret)
		if err != nil {
			return err
		}
		return publishWeb(store, prefix, assetDirectory, *mapPacks)
	}
	if *mapPacks != "" {
		return errors.New("-map-packs requires -web-only")
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

	// Hash the complete source plan before opening an object-store client. A
	// large local tree can take minutes to hash; doing this first avoids
	// leaving a manifest connection idle long enough for its keep-alive to go
	// stale before the first upload.
	objects, err := buildPlan(trees, prefix)
	if err != nil {
		return err
	}

	if *dryRun {
		targetBucket := bucketName
		if targetBucket == "" {
			targetBucket = "<bucket-not-configured>"
		}
		fmt.Printf("dry-run: %d objects would be uploaded to %s://%s/%s\n", len(objects), storageURI(provider), targetBucket, prefix)
		return nil
	}

	var store objectStore
	store, err = newObjectStore(provider, endpoint, region, bucketName, accessKeyID, accessKeySecret)
	if err != nil {
		return err
	}

	previous, err := readManifest(store, path.Join(prefix, clientManifestName))
	if err != nil {
		return err
	}
	uploaded, skipped, err := syncObjects(store, objects, previous.Objects, *workers)
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
	if err := writeManifest(store, path.Join(prefix, clientManifestName), manifest); err != nil {
		return err
	}
	revision, totalBytes := publicationRevision(objects)
	previousRevision := manifestRevision(previous)
	changedObjects, removedObjects, changedAll := publicationDelta(objects, previous.Objects, prefix)
	version := clientVersion{
		Format:         1,
		Generated:      manifest.Generated,
		Revision:       revision,
		ObjectCount:    len(objects),
		TotalBytes:     totalBytes,
		DeltaFrom:      previousRevision,
		DeltaKnown:     true,
		ChangedObjects: changedObjects,
		RemovedObjects: removedObjects,
		ChangedAll:     changedAll,
	}
	if err := publishWeb(store, prefix, assetDirectory, ""); err != nil {
		return err
	}
	/* The tiny version marker is the final publication write.  A browser that
	   observes a new revision can therefore fetch a complete _client-manifest
	   and all payload objects; it can never switch its Service Worker cache to
	   a partially uploaded tree. */
	if err := writeClientVersion(store, path.Join(prefix, clientVersionName), version); err != nil {
		return err
	}
	fmt.Printf("uploaded %d objects, skipped %d unchanged objects (total %d) to %s://%s/%s; manifest=%s revision=%s\n", uploaded, skipped, len(objects), storageURI(provider), bucketName, prefix, path.Join(prefix, clientManifestName), revision)
	return nil
}

func storageURI(provider string) string {
	if provider == "cloudflare-r2" {
		return "r2"
	}
	return "oss"
}

// credentialValue reads one credential from a secret file when configured,
// otherwise from its legacy environment variable. Secret files contain one
// value and may end with a newline (as Docker/Kubernetes secrets commonly do).
func credentialValue(valueEnv, fileEnv string) (string, error) {
	if filename := strings.TrimSpace(os.Getenv(fileEnv)); filename != "" {
		content, err := os.ReadFile(filename)
		if err != nil {
			return "", fmt.Errorf("read object-storage credential file %s: %w", fileEnv, err)
		}
		value := strings.TrimSpace(string(content))
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("object-storage credential file %s must contain one value", fileEnv)
		}
		return value, nil
	}
	return strings.TrimSpace(os.Getenv(valueEnv)), nil
}

// credentialValueAny checks provider-neutral names first, then the aliases
// used by AWS, R2 and the original Aliyun-only uploader.  This lets existing
// deployments upgrade without renaming their secret files.
func credentialValueAny(fileEnvs, valueEnvs []string) (string, error) {
	for index, fileEnv := range fileEnvs {
		valueEnv := ""
		if index < len(valueEnvs) {
			valueEnv = valueEnvs[index]
		}
		value, err := credentialValue(valueEnv, fileEnv)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
	}
	return "", nil
}

func normalizeStorageProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		provider = "aliyun-oss"
	}
	switch provider {
	case "aliyun-oss", "aliyun":
		return "aliyun-oss", nil
	case "cloudflare-r2", "r2", "cloudflare":
		return "cloudflare-r2", nil
	default:
		return "", fmt.Errorf("unsupported object-storage provider %q (supported: aliyun-oss, cloudflare-r2)", value)
	}
}

func validateStorageEndpoint(endpoint, bucketName string) error {
	if endpoint == "" || bucketName == "" {
		return errors.New("static.oss.endpoint and static.oss.bucket must be configured together")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("static.oss.endpoint must be an absolute HTTP(S) origin without credentials, path, query or fragment")
	}
	if !storageBucketNamePattern.MatchString(bucketName) {
		return fmt.Errorf("bucket %q is not a valid object-storage bucket name", bucketName)
	}
	return nil
}

func newObjectStore(provider, endpoint, region, bucketName, accessKeyID, accessKeySecret string) (objectStore, error) {
	switch provider {
	case "aliyun-oss":
		options := []oss.ClientOption{oss.Timeout(10, 60), oss.AuthVersion(oss.AuthV4), oss.Region(region)}
		client, err := oss.New(endpoint, accessKeyID, accessKeySecret, options...)
		if err != nil {
			return nil, fmt.Errorf("create Aliyun OSS client: %w", err)
		}
		bucket, err := client.Bucket(bucketName)
		if err != nil {
			return nil, fmt.Errorf("open Aliyun OSS bucket %q: %w", bucketName, err)
		}
		return aliyunObjectStore{bucket: bucket}, nil
	case "cloudflare-r2":
		// R2 exposes an S3-compatible endpoint.  Path-style addressing avoids
		// certificate/DNS issues with bucket names and works for both account
		// endpoints and local S3-compatible test servers.
		awsConfig, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(region),
			awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(accessKeyID, accessKeySecret, "")),
		)
		if err != nil {
			return nil, fmt.Errorf("create Cloudflare R2 client: %w", err)
		}
		return r2ObjectStore{client: awsS3.NewFromConfig(awsConfig, func(options *awsS3.Options) {
			options.BaseEndpoint = awsV2.String(endpoint)
			options.UsePathStyle = true
		}), bucket: bucketName}, nil
	default:
		return nil, fmt.Errorf("unsupported object-storage provider %q", provider)
	}
}

// buildPlan walks the explicit client allow-list once and hashes each file.
// Hashes are calculated before any upload starts, so the manifest is stable for
// this invocation. Each uploaded file is hashed once more after PutObject; if
// a source file is edited while a deployment is in progress, this invocation
// fails before publishing the manifest and the next retry takes a fresh plan.
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

func readManifest(store objectStore, key string) (clientManifest, error) {
	reader, err := store.GetObject(key)
	if err != nil {
		if errors.Is(err, errObjectNotFound) {
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

func syncObjects(store objectStore, objects []plannedObject, previous map[string]manifestObject, workers int) (uploaded, skipped int, err error) {
	// Browser metadata is the index which makes the rest of the client
	// addressable.  Upload it only after every image/map/audio object has
	// finished so a CDN revalidation cannot observe a new index pointing at a
	// file that is still being uploaded.  The publication manifest below is
	// written after both phases as the final commit marker.
	regular, metadata := partitionPublicationObjects(objects)
	for _, batch := range [][]plannedObject{regular, metadata} {
		batchUploaded, batchSkipped, batchErr := syncObjectBatch(store, batch, previous, workers)
		uploaded += batchUploaded
		skipped += batchSkipped
		if batchErr != nil {
			return uploaded, skipped, batchErr
		}
	}
	return uploaded, skipped, nil
}

// partitionPublicationObjects returns ordinary payloads first and browser
// indexes last.  JSON files are the generated sprite/bitmap manifests; the
// auto.dat file is the map index used by the automatic-map decoder.
func partitionPublicationObjects(objects []plannedObject) (regular, metadata []plannedObject) {
	regular = make([]plannedObject, 0, len(objects))
	metadata = make([]plannedObject, 0)
	for _, object := range objects {
		if publicationMetadataKey(object.Key) {
			metadata = append(metadata, object)
			continue
		}
		regular = append(regular, object)
	}
	return regular, metadata
}

func publicationMetadataKey(key string) bool {
	clean := path.Clean(key)
	return strings.HasSuffix(strings.ToLower(clean), ".json") || path.Base(clean) == "auto.dat"
}

func syncObjectBatch(store objectStore, objects []plannedObject, previous map[string]manifestObject, workers int) (uploaded, skipped int, err error) {
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
					// Go's mime table returns the historical `audio/x-wav` alias
					// for .wav.  Chromium/WebKit treat that alias as unsupported
					// when a CDN sends X-Content-Type-Options: nosniff, even though
					// the PCM bytes are valid.  Publish the standards spelling so
					// both the local handler and OSS/R2 objects are playable.
					if strings.EqualFold(filepath.Ext(object.Filename), ".wav") {
						contentType = "audio/wav"
					}
					if contentType == "" {
						contentType = "application/octet-stream"
					}
					cacheControl := "public, max-age=3600, must-revalidate"
					if strings.EqualFold(filepath.Ext(object.Filename), ".json") {
						cacheControl = "no-cache"
					}
					if err := putFileWithRetry(store, object.Key, object.Filename, objectMetadata{ContentType: contentType, CacheControl: cacheControl, SHA256: object.SHA256}); err != nil {
						setError(fmt.Errorf("upload %s: %w", object.Key, err))
						return
					}
					// PutObjectFromFile opens the source after buildPlan has
					// hashed it.  A running asset extraction/copy could therefore
					// replace the file between those two operations.  Do not write
					// a publication manifest that claims the old digest for the
					// newly uploaded bytes; fail this batch and let the next retry
					// take a fresh snapshot instead.
					if digest, size, digestErr := fileDigest(object.Filename); digestErr != nil {
						setError(fmt.Errorf("verify source %s after upload: %w", object.Key, digestErr))
						return
					} else if digest != object.SHA256 || size != object.Size {
						setError(fmt.Errorf("source changed while uploading %s", object.Key))
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

func writeManifest(store objectStore, key string, manifest clientManifest) error {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode client manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := putWithRetry(store, key, payload, objectMetadata{ContentType: "application/json", CacheControl: "no-cache"}); err != nil {
		return fmt.Errorf("upload client manifest %s: %w", key, err)
	}
	return nil
}

func writeClientVersion(store objectStore, key string, version clientVersion) error {
	payload, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("encode client version: %w", err)
	}
	payload = append(payload, '\n')
	if err := putWithRetry(store, key, payload, objectMetadata{ContentType: "application/json", CacheControl: "no-cache"}); err != nil {
		return fmt.Errorf("upload client version %s: %w", key, err)
	}
	return nil
}

// retryableUploadError limits retries to errors which may be resolved by
// opening a fresh connection or waiting for provider throttling to clear.
// Local source-file errors are permanent for this invocation and must not be
// hidden behind repeated attempts.
func retryableUploadError(err error) bool {
	if err == nil {
		return false
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return false
	}
	if status := uploadHTTPStatus(err); status != 0 {
		return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

// uploadHTTPStatus extracts status codes emitted by both supported storage
// clients. The OSS SDK has a value ServiceError (and also permits a pointer
// through wrappers), while the AWS SDK wraps R2 responses in ResponseError.
func uploadHTTPStatus(err error) int {
	var serviceErr oss.ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.StatusCode
	}
	var serviceErrPtr *oss.ServiceError
	if errors.As(err, &serviceErrPtr) && serviceErrPtr != nil {
		return serviceErrPtr.StatusCode
	}
	var responseErr interface{ HTTPStatusCode() int }
	if errors.As(err, &responseErr) {
		return responseErr.HTTPStatusCode()
	}
	var statusErr interface{ Got() int }
	if errors.As(err, &statusErr) {
		return statusErr.Got()
	}
	return 0
}

func uploadRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 100 * time.Millisecond
	case 2:
		return 250 * time.Millisecond
	default:
		return 500 * time.Millisecond
	}
}

func retryUpload(operationName string, operation func() error) error {
	var err error
	for attempt := 1; attempt <= maxUploadAttempts; attempt++ {
		err = operation()
		if err == nil || !retryableUploadError(err) || attempt == maxUploadAttempts {
			return err
		}
		fmt.Fprintf(os.Stderr, "stoneage-assets-sync: retrying %s (attempt %d/%d) after %s\n", operationName, attempt+1, maxUploadAttempts, uploadRetryReason(err))
		uploadRetrySleep(uploadRetryDelay(attempt))
	}
	return err
}

func putFileWithRetry(store objectStore, key, filename string, metadata objectMetadata) error {
	return retryUpload("upload "+key, func() error {
		// PutFile implementations open the source on every invocation, so a
		// retry never reuses a partially consumed file handle.
		return store.PutFile(key, filename, metadata)
	})
}

func putWithRetry(store objectStore, key string, payload []byte, metadata objectMetadata) error {
	return retryUpload("put "+key, func() error {
		return store.Put(key, payload, metadata)
	})
}

// uploadRetryReason deliberately reports only an error class or HTTP status;
// provider error strings can contain request details and are unnecessary for
// observing retry progress.
func uploadRetryReason(err error) string {
	if status := uploadHTTPStatus(err); status != 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	if errors.Is(err, io.EOF) {
		return "EOF"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected EOF"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return "network error"
	}
	return "transient error"
}

// publicationRevision derives a stable cache namespace from the complete
// sorted publication plan.  It deliberately includes the object key, size and
// digest so a rename, deletion or metadata-only replacement invalidates the
// same browser cache as a changed payload.  The object list is already sorted
// by buildPlan, but sorting a copy keeps this helper deterministic for tests
// and for callers that construct a plan directly.
func publicationRevision(objects []plannedObject) (string, int64) {
	ordered := append([]plannedObject(nil), objects...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Key < ordered[right].Key })
	hash := sha256.New()
	var total int64
	for _, object := range ordered {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", object.Key, object.Size, object.SHA256)
		if object.Size > 0 {
			total += object.Size
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), total
}

// manifestRevision computes the same namespace digest for the previous
// complete publication manifest.  An empty manifest means there is no safe
// delta base (the first publication must be treated as a full update).
func manifestRevision(manifest clientManifest) string {
	if len(manifest.Objects) == 0 {
		return ""
	}
	objects := make([]plannedObject, 0, len(manifest.Objects))
	for key, object := range manifest.Objects {
		objects = append(objects, plannedObject{Key: key, Size: object.Size, SHA256: object.SHA256})
	}
	revision, _ := publicationRevision(objects)
	return revision
}

// publicationDelta returns public object paths whose bytes changed between
// two complete publication plans.  The Service Worker uses this allow-list to
// reuse only safe entries from the previous Cache Storage namespace; an
// unknown/large delta deliberately falls back to normal network loading.
func publicationDelta(objects []plannedObject, previous map[string]manifestObject, prefix string) (changed, removed []string, changedAll bool) {
	current := make(map[string]manifestObject, len(objects))
	for _, object := range objects {
		current[object.Key] = manifestObject{Size: object.Size, SHA256: object.SHA256}
		old, exists := previous[object.Key]
		if !exists || old.Size != object.Size || old.SHA256 != object.SHA256 {
			changed = append(changed, publicObjectKey(object.Key, prefix))
		}
	}
	for key := range previous {
		if _, exists := current[key]; !exists {
			removed = append(removed, publicObjectKey(key, prefix))
		}
	}
	sort.Strings(changed)
	sort.Strings(removed)
	if len(changed)+len(removed) > clientVersionDeltaMax {
		return nil, nil, true
	}
	return changed, removed, false
}

func publicObjectKey(key, prefix string) string {
	clean := strings.TrimPrefix(path.Clean(key), "/")
	root := strings.Trim(strings.TrimSpace(prefix), "/")
	if root != "" {
		root += "/"
		clean = strings.TrimPrefix(clean, root)
	}
	return strings.TrimPrefix(path.Clean(clean), "/")
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
