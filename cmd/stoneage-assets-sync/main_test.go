package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func TestWalkTreeKeepsPublicClientAssetAllowlist(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	mapDir := filepath.Join(root, "map")
	data := filepath.Join(root, "data")
	for _, directory := range []string{assets, mapDir, filepath.Join(data, "bgm"), filepath.Join(data, "se"), filepath.Join(data, "pal")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for filename, content := range map[string]string{
		filepath.Join(assets, "manifest.json"):    "{}",
		filepath.Join(mapDir, "100.MAP"):          "map",
		filepath.Join(data, "auto.dat"):           "auto",
		filepath.Join(data, "bgm", "0.wav"):       "bgm",
		filepath.Join(data, "se", "1.wav"):        "se",
		filepath.Join(data, "pal", "Palet_1.sap"): "palette",
		filepath.Join(data, "savedata.dat"):       "private",
		filepath.Join(data, "chatreg.dat"):        "private",
	} {
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trees := []sourceTree{
		{Name: "assets", Roots: []sourceRoot{{Path: assets}}},
		{Name: "maps", Roots: []sourceRoot{{Path: mapDir}}},
		{Name: "audio", Roots: []sourceRoot{
			{Path: filepath.Join(data, "auto.dat")},
			{Path: filepath.Join(data, "bgm"), Prefix: "bgm"},
			{Path: filepath.Join(data, "se"), Prefix: "se"},
			{Path: filepath.Join(data, "pal"), Prefix: "pal"},
		}},
	}
	var got []string
	for _, tree := range trees {
		if err := walkTree(tree, func(_, relative string) error {
			got = append(got, tree.Name+"/"+filepath.ToSlash(relative))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(got)
	want := []string{"assets/manifest.json", "audio/auto.dat", "audio/bgm/0.wav", "audio/pal/Palet_1.sap", "audio/se/1.wav", "maps/100.MAP"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public upload plan = %#v, want %#v", got, want)
	}
}

func TestValidateObjectPrefix(t *testing.T) {
	for _, prefix := range []string{"", "stoneage", "client/static"} {
		if err := validateObjectPrefix(prefix); err != nil {
			t.Errorf("prefix %q rejected: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"../private", "stoneage/../private", `stoneage\\private`, "stoneage\x00private"} {
		if err := validateObjectPrefix(prefix); err == nil {
			t.Errorf("unsafe prefix %q accepted", prefix)
		}
	}
}

func TestCredentialValuePrefersSecretFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "access-key")
	if err := os.WriteFile(filename, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "environment-value")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID_FILE", filename)
	value, err := credentialValue("ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID_FILE")
	if err != nil || value != "file-value" {
		t.Fatalf("credentialValue=%q err=%v", value, err)
	}
}

func TestCredentialValueFallsBackToEnvironment(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "environment-value")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE", "")
	value, err := credentialValue("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE")
	if err != nil || value != "environment-value" {
		t.Fatalf("credentialValue=%q err=%v", value, err)
	}
}

func TestStorageProviderAliases(t *testing.T) {
	for _, value := range []string{"aliyun-oss", "aliyun"} {
		if got, err := normalizeStorageProvider(value); err != nil || got != "aliyun-oss" {
			t.Fatalf("Aliyun provider %q normalized to %q, err=%v", value, got, err)
		}
	}
	for _, value := range []string{"cloudflare-r2", "r2", "cloudflare"} {
		if got, err := normalizeStorageProvider(value); err != nil || got != "cloudflare-r2" {
			t.Fatalf("R2 provider %q normalized to %q, err=%v", value, got, err)
		}
	}
	if _, err := normalizeStorageProvider("minio"); err == nil {
		t.Fatal("unsupported object-storage provider accepted")
	}
}

func TestValidateStorageEndpoint(t *testing.T) {
	if err := validateStorageEndpoint("https://account-id.r2.cloudflarestorage.com", "stoneage-assets"); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{
		"https://user:secret@example.com",
		"https://example.com/path",
		"https://example.com?x=1",
		"ftp://example.com",
	} {
		if err := validateStorageEndpoint(endpoint, "stoneage-assets"); err == nil {
			t.Errorf("unsafe endpoint accepted: %q", endpoint)
		}
	}
}

func TestCredentialValueAnyPrefersGenericSecret(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "r2-key")
	if err := os.WriteFile(filename, []byte("generic-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE", filename)
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	value, err := credentialValueAny(
		[]string{"STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE", "AWS_ACCESS_KEY_ID_FILE"},
		[]string{"STONEAGE_ASSET_SYNC_ACCESS_KEY", "AWS_ACCESS_KEY_ID"},
	)
	if err != nil || value != "generic-key" {
		t.Fatalf("generic credential=%q err=%v", value, err)
	}
}

func TestNewObjectStoreSupportsR2S3Endpoint(t *testing.T) {
	store, err := newObjectStore("cloudflare-r2", "https://account-id.r2.cloudflarestorage.com", "auto", "stoneage-assets", "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(r2ObjectStore); !ok {
		t.Fatalf("R2 store type=%T", store)
	}
}

func TestR2ObjectStoreUsesPathStyleS3Requests(t *testing.T) {
	var receivedPath string
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.Path
		if request.Method == http.MethodPut {
			receivedBody, _ = io.ReadAll(request.Body)
			response.WriteHeader(http.StatusOK)
			return
		}
		if request.Method == http.MethodGet {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"format":1,"objects":{}}`))
			return
		}
		response.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()
	store, err := newObjectStore("cloudflare-r2", server.URL, "auto", "stoneage-assets", "id", "secret")
	if err != nil {
		t.Fatal(err)
	}
	r2 := store.(r2ObjectStore)
	if err := r2.Put("stoneage/_client-manifest.json", []byte("manifest"), objectMetadata{ContentType: "application/json", CacheControl: "no-cache"}); err != nil {
		t.Fatal(err)
	}
	if receivedPath != "/stoneage-assets/stoneage/_client-manifest.json" || !bytes.Equal(receivedBody, []byte("manifest")) {
		t.Fatalf("R2 PUT path=%q body=%q", receivedPath, receivedBody)
	}
	reader, err := r2.GetObject("stoneage/_client-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(body), `"format":1`) {
		t.Fatalf("R2 GET body=%q err=%v", body, err)
	}
}

func TestBuildPlanContainsStableHashesAndKeys(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(filepath.Join(assets, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "nested", "sprite.png"), []byte("sprite"), 0o600); err != nil {
		t.Fatal(err)
	}
	objects, err := buildPlan([]sourceTree{{Name: "assets", Roots: []sourceRoot{{Path: assets}}}}, "stoneage")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "stoneage/assets/nested/sprite.png" || objects[0].Size != 6 {
		t.Fatalf("planned objects = %#v", objects)
	}
	if objects[0].SHA256 != "4a046e33ecf7aced9bfd000747bb1fda7836c8ceeff662af33c2a2c288b4e78c" {
		t.Fatalf("unexpected digest %q", objects[0].SHA256)
	}
}

func TestPartitionPublicationObjectsUploadsPayloadBeforeIndexes(t *testing.T) {
	objects := []plannedObject{
		{Key: "stoneage/assets/manifest.json"},
		{Key: "stoneage/assets/bitmaps/bitmap_1.png"},
		{Key: "stoneage/audio/auto.dat"},
		{Key: "stoneage/maps/100.MAP"},
		{Key: "stoneage/assets/sprites.json"},
	}
	regular, metadata := partitionPublicationObjects(objects)
	if got := []string{regular[0].Key, regular[1].Key}; !reflect.DeepEqual(got, []string{"stoneage/assets/bitmaps/bitmap_1.png", "stoneage/maps/100.MAP"}) {
		t.Fatalf("regular publication objects = %#v", got)
	}
	if got := []string{metadata[0].Key, metadata[1].Key, metadata[2].Key}; !reflect.DeepEqual(got, []string{"stoneage/assets/manifest.json", "stoneage/audio/auto.dat", "stoneage/assets/sprites.json"}) {
		t.Fatalf("metadata publication objects = %#v", got)
	}
}

func TestPublicationMetadataKeyOnlyMatchesIndexes(t *testing.T) {
	for _, key := range []string{"stoneage/assets/manifest.json", "stoneage/audio/auto.dat", "stoneage/assets/nested/SPRITES.JSON"} {
		if !publicationMetadataKey(key) {
			t.Errorf("publication metadata key %q was not recognized", key)
		}
	}
	for _, key := range []string{"stoneage/assets/bitmap_1.png", "stoneage/maps/100.MAP", "stoneage/audio/bgm/0.wav"} {
		if publicationMetadataKey(key) {
			t.Errorf("payload key %q was recognized as metadata", key)
		}
	}
}

func TestClientManifestRoundTrips(t *testing.T) {
	manifest := clientManifest{Format: 1, Generated: "2026-08-30T00:00:00Z", Objects: map[string]manifestObject{
		"stoneage/assets/a": {Size: 3, SHA256: "abc"},
	}}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded clientManifest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest, decoded) {
		t.Fatalf("manifest round trip = %#v, want %#v", decoded, manifest)
	}
}

type memoryObjectStore struct {
	objects  map[string][]byte
	metadata map[string]objectMetadata
}

type scriptedObjectStore struct {
	putFileCalls int
	putCalls     int
	putFileErr   func(attempt int) error
	putErr       func(attempt int) error
}

func (store *scriptedObjectStore) GetObject(string) (io.ReadCloser, error) {
	return nil, errObjectNotFound
}

func (store *scriptedObjectStore) PutFile(_, _ string, _ objectMetadata) error {
	store.putFileCalls++
	if store.putFileErr != nil {
		return store.putFileErr(store.putFileCalls)
	}
	return nil
}

func (store *scriptedObjectStore) Put(_ string, _ []byte, _ objectMetadata) error {
	store.putCalls++
	if store.putErr != nil {
		return store.putErr(store.putCalls)
	}
	return nil
}

func (store *memoryObjectStore) GetObject(key string) (io.ReadCloser, error) {
	payload, ok := store.objects[key]
	if !ok {
		return nil, errObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (store *memoryObjectStore) PutFile(key, filename string, metadata objectMetadata) error {
	payload, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	return store.Put(key, payload, metadata)
}

func (store *memoryObjectStore) Put(key string, payload []byte, metadata objectMetadata) error {
	if store.objects == nil {
		store.objects = make(map[string][]byte)
	}
	if store.metadata == nil {
		store.metadata = make(map[string]objectMetadata)
	}
	store.objects[key] = append([]byte(nil), payload...)
	store.metadata[key] = metadata
	return nil
}

func TestPublicationRevisionIsStableAndIncludesDeletes(t *testing.T) {
	objects := []plannedObject{
		{Key: "stoneage/assets/b.png", Size: 2, SHA256: "bbb"},
		{Key: "stoneage/assets/a.png", Size: 3, SHA256: "aaa"},
	}
	revision, total := publicationRevision(objects)
	if total != 5 {
		t.Fatalf("total bytes=%d, want 5", total)
	}
	// Ordering the input differently must not change the publication namespace.
	reordered, reorderedTotal := publicationRevision([]plannedObject{objects[1], objects[0]})
	if revision == "" || revision != reordered || reorderedTotal != total {
		t.Fatalf("revision changed with ordering: %q/%q totals %d/%d", revision, reordered, total, reorderedTotal)
	}
	changed, _ := publicationRevision(append(objects, plannedObject{Key: "stoneage/maps/1.DAT", Size: 0, SHA256: ""}))
	if revision == changed {
		t.Fatalf("adding an object did not invalidate revision %q", revision)
	}
}

func TestPublicationDeltaUsesPublicKeysAndHashChanges(t *testing.T) {
	previous := map[string]manifestObject{
		"stoneage/assets/unchanged.png": {Size: 2, SHA256: "same"},
		"stoneage/assets/changed.png":   {Size: 2, SHA256: "old"},
		"stoneage/audio/removed.wav":    {Size: 4, SHA256: "gone"},
	}
	objects := []plannedObject{
		{Key: "stoneage/assets/unchanged.png", Size: 2, SHA256: "same"},
		{Key: "stoneage/assets/changed.png", Size: 2, SHA256: "new"},
		{Key: "stoneage/maps/new.map", Size: 3, SHA256: "new-map"},
	}
	changed, removed, all := publicationDelta(objects, previous, "stoneage")
	if all || !reflect.DeepEqual(changed, []string{"assets/changed.png", "maps/new.map"}) || !reflect.DeepEqual(removed, []string{"audio/removed.wav"}) {
		t.Fatalf("publication delta = changed=%#v removed=%#v all=%t", changed, removed, all)
	}
}

func TestManifestRevisionMatchesPublicationRevision(t *testing.T) {
	objects := []plannedObject{
		{Key: "stoneage/assets/a.png", Size: 3, SHA256: "aaa"},
		{Key: "stoneage/maps/1.map", Size: 4, SHA256: "bbb"},
	}
	want, _ := publicationRevision(objects)
	manifest := clientManifest{Format: 1, Objects: map[string]manifestObject{
		objects[1].Key: {Size: objects[1].Size, SHA256: objects[1].SHA256},
		objects[0].Key: {Size: objects[0].Size, SHA256: objects[0].SHA256},
	}}
	if got := manifestRevision(manifest); got != want {
		t.Fatalf("manifest revision=%q, want %q", got, want)
	}
	if got := manifestRevision(clientManifest{Format: 1, Objects: map[string]manifestObject{}}); got != "" {
		t.Fatalf("empty manifest revision=%q, want empty", got)
	}
}

func TestPublicationDeltaMarksLargeChangesAsUnknown(t *testing.T) {
	previous := make(map[string]manifestObject, clientVersionDeltaMax)
	objects := make([]plannedObject, clientVersionDeltaMax+1)
	for index := range objects {
		key := "stoneage/assets/file-" + strconv.Itoa(index) + ".png"
		objects[index] = plannedObject{Key: key, Size: 1, SHA256: "new"}
		previous[key] = manifestObject{Size: 1, SHA256: "old"}
	}
	changed, removed, all := publicationDelta(objects, previous, "stoneage")
	if !all || changed != nil || removed != nil {
		t.Fatalf("large publication delta = changed=%#v removed=%#v all=%t", changed, removed, all)
	}
}

func TestWriteClientVersionPublishesNoCacheJSON(t *testing.T) {
	store := &memoryObjectStore{}
	want := clientVersion{Format: 1, Generated: "2026-08-30T00:00:00Z", Revision: "0123456789abcdef", ObjectCount: 12, TotalBytes: 3456, DeltaFrom: "fedcba9876543210", DeltaKnown: true, ChangedObjects: []string{"assets/new.png"}, RemovedObjects: []string{"maps/old.map"}}
	if err := writeClientVersion(store, "stoneage/_client-version.json", want); err != nil {
		t.Fatal(err)
	}
	if got := store.metadata["stoneage/_client-version.json"]; got.ContentType != "application/json" || got.CacheControl != "no-cache" {
		t.Fatalf("version metadata=%#v", got)
	}
	var decoded clientVersion
	if err := json.Unmarshal(store.objects["stoneage/_client-version.json"], &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("version=%#v, want %#v", decoded, want)
	}
}

func withoutUploadRetrySleep(t *testing.T) {
	t.Helper()
	previous := uploadRetrySleep
	uploadRetrySleep = func(time.Duration) {}
	t.Cleanup(func() { uploadRetrySleep = previous })
}

func TestPutFileWithRetryRetriesUnexpectedEOF(t *testing.T) {
	withoutUploadRetrySleep(t)
	store := &scriptedObjectStore{putFileErr: func(attempt int) error {
		if attempt < 3 {
			return io.ErrUnexpectedEOF
		}
		return nil
	}}
	if err := putFileWithRetry(store, "stoneage/assets/sprite.png", "/tmp/sprite.png", objectMetadata{}); err != nil {
		t.Fatal(err)
	}
	if store.putFileCalls != 3 {
		t.Fatalf("PutFile calls=%d, want 3", store.putFileCalls)
	}
}

func TestPutFileWithRetryStopsAfterFourServerErrors(t *testing.T) {
	withoutUploadRetrySleep(t)
	store := &scriptedObjectStore{putFileErr: func(int) error {
		return oss.ServiceError{StatusCode: 503}
	}}
	if err := putFileWithRetry(store, "stoneage/assets/sprite.png", "/tmp/sprite.png", objectMetadata{}); err == nil {
		t.Fatal("persistent server error unexpectedly succeeded")
	}
	if store.putFileCalls != maxUploadAttempts {
		t.Fatalf("PutFile calls=%d, want %d", store.putFileCalls, maxUploadAttempts)
	}
}

func TestPutFileWithRetryDoesNotRetryForbiddenOrLocalFileErrors(t *testing.T) {
	for name, failure := range map[string]error{
		"forbidden":  oss.ServiceError{StatusCode: 403},
		"local file": &os.PathError{Op: "open", Path: "missing", Err: os.ErrNotExist},
	} {
		t.Run(name, func(t *testing.T) {
			withoutUploadRetrySleep(t)
			store := &scriptedObjectStore{putFileErr: func(int) error { return failure }}
			if err := putFileWithRetry(store, "stoneage/assets/sprite.png", "/tmp/sprite.png", objectMetadata{}); err == nil {
				t.Fatal("permanent upload error unexpectedly succeeded")
			}
			if store.putFileCalls != 1 {
				t.Fatalf("PutFile calls=%d, want 1", store.putFileCalls)
			}
		})
	}
}

func TestWriteClientVersionUsesUploadRetry(t *testing.T) {
	withoutUploadRetrySleep(t)
	store := &scriptedObjectStore{putErr: func(attempt int) error {
		if attempt < 3 {
			return io.EOF
		}
		return nil
	}}
	if err := writeClientVersion(store, "stoneage/_client-version.json", clientVersion{Format: 1}); err != nil {
		t.Fatal(err)
	}
	if store.putCalls != 3 {
		t.Fatalf("Put calls=%d, want 3", store.putCalls)
	}
}

// The uploader reads the same strict TOML schema as Web. Adding an unrelated
// Web setting must not break the admin's real asset-sync command.
func TestUploaderAcceptsShippedWebConfigWithServerDirectory(t *testing.T) {
	config, err := loadConfig(filepath.Join("..", "..", "config", "web", "web.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.GatewayAPIURL != "http://gateway:9080" {
		t.Fatalf("gateway URL=%q", config.GatewayAPIURL)
	}
}
