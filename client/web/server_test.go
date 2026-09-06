package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(upstream string) Config {
	cfg := DefaultConfig()
	cfg.TCPUpstream = upstream
	cfg.PollTimeout = 200 * time.Millisecond
	cfg.IdleTimeout = 2 * time.Second
	cfg.DialTimeout = time.Second
	cfg.MaxSessions = 4
	return cfg
}

func TestDecodeLegacyNPCTextMixedCodePages(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "generated cp936 mammoth", raw: []byte{0xb3, 0xa4, 0xc3, 0xab, 0xcf, 0xf3, 0xbf, 0xcd, 0xd4, 0xcb}, want: "长毛象客运"},
		{name: "legacy big5 mammoth bus", raw: []byte{0xaa, 0xf8, 0xa4, 0xf2, 0xb6, 0x48, 0xa4, 0xbd, 0xa8, 0xae}, want: "長毛象公車"},
		{name: "ascii", raw: []byte("npcgen_man"), want: "npcgen_man"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := decodeLegacyNPCText(test.raw); got != test.want {
				t.Fatalf("decodeLegacyNPCText()=%q want %q", got, test.want)
			}
		})
	}
}

func TestDecodeLegacyNPCTextChoosesEncodingPerLine(t *testing.T) {
	// chatroom.create contains CP936 ticket names and Big5 mammoth names in
	// the same file.  A whole-file decoder must not let one encoding poison
	// the other; the parser consumes only the field lines after decoding.
	raw := bytes.Join([][]byte{
		[]byte("# Japanese comment (mixed legacy bytes)"),
		[]byte("name=\xc3\xc5\xc6\xb1\xb7\xb7\xc2\xf4\xd4\xb1"), // 门票贩卖员 (GBK)
		[]byte("name=\xaa\xf8\xa4\xf2\xb6\x48\xa4\xbd\xa8\xae"), // 長毛象公車 (Big5)
	}, []byte{'\n'})
	decoded := decodeLegacyNPCText(raw)
	if !strings.Contains(decoded, "name=门票贩卖员") || !strings.Contains(decoded, "name=長毛象公車") {
		t.Fatalf("per-line legacy decode lost mixed names: %q", decoded)
	}
}

func TestNPCInteractionRangeFromCreateArg(t *testing.T) {
	tests := []struct {
		name  string
		enemy string
		want  int
	}{
		{name: "range one", enemy: "npcgen_winhealer|1|4.0|6.0|1", want: 1},
		{name: "range two", enemy: "npcgen_winhealer|1|4.0|6.0|2", want: 2},
		{name: "missing defaults to one", enemy: "npcgen_winhealer|1|4.0|6.0", want: 1},
		{name: "zero defaults to one", enemy: "windowhealer|1|4.0|6.0|0", want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			template := npcTemplateFromEnemy(test.enemy)
			if got := npcInteractionRange(template, test.enemy); got != test.want {
				t.Fatalf("npcInteractionRange(%q)=%d want %d", test.enemy, got, test.want)
			}
		})
	}
}

type fakeTCP struct {
	listener net.Listener
	accepted chan struct{}
	received chan []byte
	done     chan struct{}
	online   sync.Once
}

func newFakeTCP(t *testing.T, greeting []byte, reply []byte) *fakeTCP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTCP{
		listener: listener,
		accepted: make(chan struct{}),
		received: make(chan []byte, 1),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(fake.done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		close(fake.accepted)
		_, _ = connection.Write(greeting)
		reader := bufio.NewReader(connection)
		packet, readErr := reader.ReadBytes('\n')
		if readErr == nil {
			fake.received <- packet
			if reply != nil {
				_, _ = connection.Write(reply)
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-fake.done:
		case <-time.After(time.Second):
			t.Error("fake TCP server did not stop")
		}
	})
	return fake
}

// newGreetingThenCloseTCP models the upstream disappearing between the
// gateway handshake and the unload-time CharLogout write.  The HTTP bridge
// must still remove the browser session when the close=1 write cannot reach
// the socket; otherwise a refresh would leak a session until idle expiry.
func newGreetingThenCloseTCP(t *testing.T, greeting []byte) *fakeTCP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTCP{
		listener: listener,
		accepted: make(chan struct{}),
		received: make(chan []byte, 1),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(fake.done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		close(fake.accepted)
		_, _ = connection.Write(greeting)
		_ = connection.Close()
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-fake.done:
		case <-time.After(time.Second):
			t.Error("fake TCP close server did not stop")
		}
	})
	return fake
}

func (fake *fakeTCP) address() string { return fake.listener.Addr().String() }

func TestReadDelimitedPacket(t *testing.T) {
	packet, err := readDelimitedPacket(bufio.NewReader(strings.NewReader("abc\nrest")), 16)
	if err != nil || string(packet) != "abc\n" {
		t.Fatalf("packet=%q err=%v", packet, err)
	}
	_, err = readDelimitedPacket(bufio.NewReader(strings.NewReader("123456\n")), 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize packet error=%v", err)
	}
	_, err = readDelimitedPacket(bufio.NewReader(strings.NewReader("partial")), 16)
	if err == nil {
		t.Fatal("partial packet unexpectedly accepted")
	}
}

func TestHandlerServesPageAndHealth(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("StoneAge")) {
		t.Fatalf("page status=%d body prefix=%q", response.StatusCode, body[:minInt(len(body), 80)])
	}
	response, err = http.Get(server.URL + "/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || manifest["orientation"] != "any" || manifest["display"] != "fullscreen" {
		t.Fatalf("manifest status=%d body=%v", response.StatusCode, manifest)
	}
	response, err = http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || health["status"] != "ok" {
		t.Fatalf("health status=%d body=%v", response.StatusCode, health)
	}
	response, err = http.Get(server.URL + "/_client-version.json")
	if err != nil {
		t.Fatal(err)
	}
	var version map[string]any
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || version["revision"] != "local-dev" || version["changed_all"] != true {
		t.Fatalf("local asset version status=%d body=%v", response.StatusCode, version)
	}
}

func TestHandlerRewritesOnlyStaticResourcesToCDN(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	cfg := testConfig(fake.address())
	cfg.CDNBaseURL = "https://cdn.example.com/stoneage/"
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("page status=%d", response.StatusCode)
	}
	text := string(body)
	base := "https://cdn.example.com/stoneage"
	for _, fragment := range []string{
		base + "/assets/bitmaps/bitmap_9113.png",
		base + "/assets/manifest.json",
		base + "/maps/${encodeURIComponent(file)}",
		base + "/audio/${kind}/${encodeURIComponent(String(file||\"\"))}",
		base + "/audio/auto.dat",
		`const ASSET_RESOURCE_ROOT=new URL("` + base + `/assets/",window.location.href)`,
		`for(const root of STATIC_RESOURCE_ROOTS)`,
		`url.origin!==root.origin`,
		`url.pathname.startsWith(root.pathname)`,
		`image=new Image();image.crossOrigin="anonymous";image.decoding="async"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("CDN page lost rewritten static fragment %q", fragment)
		}
	}
	for _, fragment := range []string{`src="/assets/`, `fetch("/audio/`, "fetch(`/maps/"} {
		if strings.Contains(text, fragment) {
			t.Errorf("CDN page retained origin-relative static fragment %q", fragment)
		}
	}
	for _, fragment := range []string{`this.base+"/api/sessions"`, "fetch(`/api/npcs?floor="} {
		if !strings.Contains(text, fragment) {
			t.Errorf("CDN page rewrote origin API fragment %q", fragment)
		}
	}
	if handler.config.CDNBaseURL != base {
		t.Fatalf("normalized CDN base=%q want %q", handler.config.CDNBaseURL, base)
	}
	// Relative URL construction must remain valid after the server's static
	// prefix rewrite.  A literal "../maps/" would be rewritten into an invalid
	// "..https://cdn..." string.
	for _, fragment := range []string{
		`const ASSET_RESOURCE_ROOT=new URL("https://cdn.example.com/stoneage/assets/",window.location.href)`,
		`new URL("maps/",STATIC_RESOURCE_BASE)`,
		`new URL("audio/",STATIC_RESOURCE_BASE)`,
		`const ASSET_VERSION_URL=new URL("_client-version.json",STATIC_RESOURCE_BASE)`,
	} {
		if !strings.Contains(string(handler.page), fragment) {
			t.Errorf("CDN page lost valid relative resource fragment %q", fragment)
		}
	}
	if strings.Contains(string(handler.page), "..https://cdn.example.com") {
		t.Fatal("CDN page contains malformed relative URL")
	}
}

func TestPageWithCDNBaseDoesNotRewriteInsertedRoot(t *testing.T) {
	// A CDN is allowed to publish under a path that happens to contain one of
	// the client's tree names.  Rewriting /assets/, /maps/ and /audio/ in a
	// single pass used to reprocess that inserted path and duplicate segments.
	base := "https://cdn.example.com/game/maps/audio"
	source := []byte(`assets=/assets/a.png maps=/maps/1000.MAP audio=/audio/bgm/sabgm_s0.wav`)
	got := string(pageWithCDNBase(source, base))
	want := `assets=https://cdn.example.com/game/maps/audio/assets/a.png maps=https://cdn.example.com/game/maps/audio/maps/1000.MAP audio=https://cdn.example.com/game/maps/audio/audio/bgm/sabgm_s0.wav`
	if got != want {
		t.Fatalf("CDN root was rewritten more than once:\n got %q\nwant %q", got, want)
	}
}

func TestAutoMapFilesIndexPreservesCaseAndExcludesUnsafeEntries(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{
		"200.DAT", "201.dat", "202.MAP", "203.map", "204.json", "205.DAT", "205.map", "205.DAT.bak", "20x.DAT",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "206.DAT"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "200.DAT"), filepath.Join(directory, "207.DAT")); err != nil {
		if os.IsPermission(err) {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	want := map[string][]string{
		"200": {"200.DAT"},
		"201": {"201.dat"},
		"202": {"202.MAP"},
		"203": {"203.map"},
		"205": {"205.DAT", "205.map"},
	}
	if got := autoMapFilesForDirectory(directory); !reflect.DeepEqual(got, want) {
		t.Fatalf("auto map file index=%v want %v", got, want)
	}

	page := pageWithAutoMapFiles([]byte("const AUTO_MAP_FILES={};"), want)
	if strings.Contains(string(page), "207.DAT") || strings.Contains(string(page), "204.json") || strings.Contains(string(page), "205.DAT.bak") {
		t.Fatalf("unsafe map filename leaked into page: %s", page)
	}
	var encoded map[string][]string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(string(page), "const AUTO_MAP_FILES="), ";")), &encoded); err != nil {
		t.Fatalf("embedded map index is not JSON: %v; page=%s", err, page)
	}
	if !reflect.DeepEqual(encoded, want) {
		t.Fatalf("embedded map index=%v want %v", encoded, want)
	}
}

func TestAutoMapFilesIndexMissingDirectoryIsEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got := autoMapFilesForDirectory(missing); len(got) != 0 {
		t.Fatalf("missing map directory index=%v want empty", got)
	}
	page := pageWithAutoMapFiles([]byte("const AUTO_MAP_FILES={};"), nil)
	if got := string(page); got != "const AUTO_MAP_FILES={};" {
		t.Fatalf("empty map index page=%q", got)
	}
}

func TestCDNBaseComesFromEnvironmentAndRejectsUnsafeURLs(t *testing.T) {
	t.Setenv("STONEAGE_WEB_CDN_BASE_URL", "https://cdn.example.com/stoneage/")
	if got := configFromEnvironment().CDNBaseURL; got != "https://cdn.example.com/stoneage/" {
		t.Fatalf("environment CDN base=%q", got)
	}
	for _, value := range []string{
		"//cdn.example.com/stoneage",
		"ftp://cdn.example.com/stoneage",
		"https://user:secret@cdn.example.com/stoneage",
		"https://cdn.example.com/stoneage?release=v1",
		"https://cdn.example.com/stoneage#release",
		`https://cdn.example.com/'></style>`,
	} {
		if _, err := normalizeCDNBaseURL(value); err == nil {
			t.Errorf("unsafe CDN base accepted: %q", value)
		}
	}
}

func TestWebConfigFileLoadsOSSAndCDNWithEnvironmentOverride(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "web.toml")
	content := `listen_address = "127.0.0.1:18089"
tcp_upstream = "127.0.0.1:19065"
max_sessions = 23
poll_timeout = "9s"

[static]
assets_directory = "/srv/stoneage/assets"
maps_directory = "/srv/stoneage/maps"
audio_directory = "/srv/stoneage/audio"
npc_directory = "/srv/stoneage/npc"

[static.oss]
provider = "aliyun-oss"
endpoint = "https://oss-cn-shanghai.aliyuncs.com/"
region = "cn-shanghai"
bucket = "stoneage-web-assets"
prefix = "/stoneage/"

[static.cdn]
base_url = "https://cdn.example.com/stoneage/"
`
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STONEAGE_WEB_CONFIG", "")
	t.Setenv("STONEAGE_WEB_CDN_BASE_URL", "")
	t.Setenv("STONEAGE_WEB_MAX_SESSIONS", "")
	cfg, path, err := configFromCommandLine([]string{"-config", filename})
	if err != nil {
		t.Fatal(err)
	}
	if path != filename || cfg.ListenAddress != "127.0.0.1:18089" || cfg.TCPUpstream != "127.0.0.1:19065" || cfg.MaxSessions != 23 || cfg.PollTimeout != 9*time.Second || cfg.CDNBaseURL != "https://cdn.example.com/stoneage/" {
		t.Fatalf("loaded config path=%q cfg=%+v", path, cfg)
	}
	oss, err := normalizeOSSConfig(cfg.OSS)
	if err != nil {
		t.Fatal(err)
	}
	if oss.Endpoint != "https://oss-cn-shanghai.aliyuncs.com" || oss.Region != "cn-shanghai" || oss.Bucket != "stoneage-web-assets" || oss.Prefix != "stoneage" {
		t.Fatalf("normalized OSS=%+v", oss)
	}
	if got := ossPublicBaseURL(oss); got != "https://stoneage-web-assets.oss-cn-shanghai.aliyuncs.com/stoneage" {
		t.Fatalf("public OSS base=%q", got)
	}
	t.Setenv("STONEAGE_WEB_CDN_BASE_URL", "https://emergency.example.com/stoneage")
	overridden, _, err := configFromCommandLine([]string{"-config", filename})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.CDNBaseURL != "https://emergency.example.com/stoneage" {
		t.Fatalf("CDN environment override=%q", overridden.CDNBaseURL)
	}
}

func TestHandlerUsesPublicOSSRootWhenCDNIsUnset(t *testing.T) {
	cfg := testConfig("127.0.0.1:1")
	cfg.CDNBaseURL = ""
	cfg.OSS.Endpoint = "https://oss-cn-hangzhou.aliyuncs.com"
	cfg.OSS.Region = "cn-hangzhou"
	cfg.OSS.Bucket = "stoneage-web-assets"
	cfg.OSS.Prefix = "stoneage"
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	text := string(handler.page)
	for _, fragment := range []string{
		"https://stoneage-web-assets.oss-cn-hangzhou.aliyuncs.com/stoneage/assets/",
		"https://stoneage-web-assets.oss-cn-hangzhou.aliyuncs.com/stoneage/maps/",
		"https://stoneage-web-assets.oss-cn-hangzhou.aliyuncs.com/stoneage/audio/",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("OSS-backed page lost rewritten static fragment %q", fragment)
		}
	}
}

func TestCloudflareR2ConfigUsesCDNWithoutExposingS3Endpoint(t *testing.T) {
	value, err := normalizeOSSConfig(OSSConfig{
		Provider: "cloudflare-r2",
		Endpoint: "https://account-id.r2.cloudflarestorage.com/",
		Bucket:   "stoneage-assets",
		Prefix:   "/stoneage/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Provider != "cloudflare-r2" || value.Region != "auto" {
		t.Fatalf("normalized R2 config=%+v", value)
	}
	if got := ossPublicBaseURL(value); got != "" {
		t.Fatalf("R2 S3 endpoint was exposed as public asset root %q", got)
	}
	cfg := testConfig("127.0.0.1:1")
	cfg.OSS = value
	cfg.CDNBaseURL = "https://cdn.example.com/stoneage"
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if !strings.Contains(string(handler.page), "https://cdn.example.com/stoneage/assets/") {
		t.Fatal("R2 CDN base was not used for browser assets")
	}
	if strings.Contains(string(handler.page), "r2.cloudflarestorage.com") {
		t.Fatal("R2 S3 endpoint leaked into browser page")
	}
}

func TestWebConfigFileAndOSSValidationFailClosed(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "web.toml")
	if err := os.WriteFile(filename, []byte("[static.cdn]\ndomain = \"https://cdn.example.com\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWebConfigFile(filename); err == nil || !strings.Contains(err.Error(), "missing in the target struct") {
		t.Fatalf("unknown config field error=%v", err)
	}
	for _, value := range []OSSConfig{
		{Provider: "aliyun-oss", Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Prefix: "stoneage"},
		{Provider: "aliyun-oss", Endpoint: "https://oss-cn-hangzhou.aliyuncs.com/path", Bucket: "stoneage-web-assets", Prefix: "stoneage"},
		{Provider: "aliyun-oss", Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Bucket: "StoneAge", Prefix: "stoneage"},
		{Provider: "aliyun-oss", Endpoint: "https://oss-cn-hangzhou.aliyuncs.com", Bucket: "stoneage-web-assets", Prefix: "stoneage/../v1"},
	} {
		if _, err := normalizeOSSConfig(value); err == nil {
			t.Errorf("invalid OSS configuration accepted: %+v", value)
		}
	}
}

func TestWebConfigRejectsJSONInFavorOfTOML(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "web.json")
	if err := os.WriteFile(filename, []byte(`{"listen_address":"127.0.0.1:18089"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadWebConfigFile(filename); err == nil {
		t.Fatal("JSON web config unexpectedly accepted; use TOML")
	}
}

func TestEmbeddedPageKeepsLegacyLoginServerCharacterFlow(t *testing.T) {
	body := string(page)
	required := []string{
		`<section id="server-screen" class="hidden" data-stage="group">`,
		`<section id="login-screen">`,
		`show(loginScreen);`,
		`acceptLoginCredentials();`,
		`serverSelectionStage==="connecting"`,
		`app.selectedServer="local-line"`,
		`url('/assets/bitmaps/bitmap_9094.png')`,
		`data-src="/assets/bitmaps/bitmap_9103.png"`,
		`data-src="/assets/bitmaps/bitmap_9111.png"`,
		`left:256px; top:270px; width:128px; height:144px`,
		`left:238px; top:249px; width:124px; height:68px`,
		`left:276px; top:421px`,
		`data-action="card" data-src="/assets/bitmaps/bitmap_9221.png"`,
		`send("TK",[app.position[0],app.position[1],"P|hi",0,3])`,
		`interactive-widget=overlays-content`,
		`autocapitalize="none"`,
		`账号须为 1–15 位英文字母、数字、下划线、连字符或点`,
		`密码须为 1–12 位半角英文字母、数字或符号，不支持空格`,
		`font-size:16px !important`,
		`navigator.serviceWorker.register("/sw.js",{scope:"/"})`,
		`const ASSET_VERSION_URL=new URL("_client-version.json",STATIC_RESOURCE_BASE)`,
		`type:"set-asset-version"`,
		`type:"prefetch-assets"`,
		`id="world-loading-progress"`,
		`loadingActive=Boolean(app.mapLoading)&&app.phase==="world"`,
		`previous window is complete`,
		`const first=Number(parts[0])`,
		`const charType=first,id=Protocol.base62(parts[1]||"0")`,
		`actor.charType`,
		`empty CD payload`,
		`if(actor&&!actor.staticNPC&&actor.kind==="character")restoreStaticNPCAt(actor.x,actor.y);`,
	}
	for _, fragment := range required {
		if !strings.Contains(body, fragment) {
			t.Errorf("embedded page lost legacy entry fragment %q", fragment)
		}
	}
	forbidden := []string{`class="server-entry`, `id="server-ok"`, `id="server-quit"`, `show(serverScreen);\n  renderWorld()`, `if(!ids.length)app.actors.clear()`}
	for _, fragment := range forbidden {
		if strings.Contains(body, fragment) {
			t.Errorf("embedded page contains obsolete server-first UI fragment %q", fragment)
		}
	}
}

func TestHandlerServesConfiguredAssets(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "manifest.json"), []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(fake.address())
	cfg.AssetsDirectory = assets
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/assets/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != `{"format":1}` {
		t.Fatalf("asset status=%d body=%q", response.StatusCode, body)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("asset manifest cache header=%q", got)
	}
}

func TestHandlerServesServiceWorkerWithRootScope(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	cfg := testConfig(fake.address())
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("worker status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if response.Header.Get("Cache-Control") != "no-cache" || response.Header.Get("Service-Worker-Allowed") != "/" {
		t.Fatalf("worker headers cache=%q allowed=%q", response.Header.Get("Cache-Control"), response.Header.Get("Service-Worker-Allowed"))
	}
	for _, fragment := range []string{"CACHE_PREFIX", "set-asset-version", "prefetch-assets", "event.respondWith"} {
		if !strings.Contains(string(body), fragment) {
			t.Errorf("worker body missing %q", fragment)
		}
	}
}

func TestHandlerServesConfiguredMaps(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	maps := t.TempDir()
	if err := os.WriteFile(filepath.Join(maps, "1011.DAT"), []byte("map-data"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(fake.address())
	cfg.MapDirectory = maps
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/maps/1011.DAT")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "map-data" {
		t.Fatalf("map status=%d body=%q", response.StatusCode, body)
	}
	if !strings.Contains(response.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("map cache header=%q", response.Header.Get("Cache-Control"))
	}
}

func TestHandlerServesConfiguredAudio(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	audio := t.TempDir()
	if err := os.Mkdir(filepath.Join(audio, "bgm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(audio, "bgm", "test.wav"), []byte("RIFF-test"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(fake.address())
	cfg.AudioDirectory = audio
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/audio/bgm/test.wav")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "RIFF-test" {
		t.Fatalf("audio status=%d body=%q", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("audio content type=%q", got)
	}
}

func TestHTTPSessionForwardsGreetingPacketsAndClose(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, []byte("reply\n"))
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/sessions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || created.ID == "" || created.Greeting != "TAA=" {
		t.Fatalf("create status=%d response=%+v", response.StatusCode, created)
	}
	packet := []byte("client-packet\n")
	encoded, _ := json.Marshal(sendRequest{Packet: base64.StdEncoding.EncodeToString(packet)})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/sessions/"+created.ID+"/send", bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("send status=%d", response.StatusCode)
	}
	select {
	case received := <-fake.received:
		if !bytes.Equal(received, packet) {
			t.Fatalf("upstream received=%q want=%q", received, packet)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive packet")
	}
	response, err = http.Get(server.URL + "/api/sessions/" + created.ID + "/events?timeout=1000")
	if err != nil {
		t.Fatal(err)
	}
	var events struct {
		Events []eventResponse `json:"events"`
		Closed bool            `json:"closed"`
	}
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(events.Events) == 0 || events.Events[0].Packet == "" {
		t.Fatalf("events status=%d response=%+v", response.StatusCode, events)
	}
	if got, _ := base64.StdEncoding.DecodeString(events.Events[0].Packet); !bytes.Equal(got, []byte("reply\n")) {
		t.Fatalf("reply=%q", got)
	}
	closedEvent := false
	for _, event := range events.Events {
		closedEvent = closedEvent || event.Closed
	}
	/* The reply and the peer FIN are ordered on TCP, but readLoop necessarily
	   discovers EOF with the read after the newline-delimited reply.  A poll
	   may therefore return the queued reply just before finish() publishes the
	   close event.  That is the real browser contract: consume the reply, then
	   immediately poll again.  Requiring both in one HTTP response made this
	   test depend on goroutine scheduling instead of proving close delivery. */
	if !closedEvent && !events.Closed {
		response, err = http.Get(server.URL + "/api/sessions/" + created.ID + "/events?timeout=1000")
		if err != nil {
			t.Fatal(err)
		}
		var followup struct {
			Events []eventResponse `json:"events"`
			Closed bool            `json:"closed"`
		}
		if err := json.NewDecoder(response.Body).Decode(&followup); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("follow-up events status=%d response=%+v", response.StatusCode, followup)
		}
		for _, event := range followup.Events {
			closedEvent = closedEvent || event.Closed
		}
		if !closedEvent && !followup.Closed {
			t.Fatalf("upstream close was not forwarded: first=%+v followup=%+v", events, followup)
		}
	}
	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/api/sessions/"+created.ID, nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.StatusCode)
	}
}

func TestHTTPSessionGracefulDeleteWaitsForPeerClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerSawEOF := make(chan struct{})
	releasePeer := make(chan struct{})
	peerDone := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releasePeer) }) }
	t.Cleanup(func() {
		release()
		_ = listener.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("graceful-close TCP peer did not stop")
		}
	})
	go func() {
		defer close(peerDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte{'L', 0})
		_, _ = io.Copy(io.Discard, connection)
		close(peerSawEOF)
		<-releasePeer
	}()

	handler, err := NewHandler(testConfig(listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}

	deleteDone := make(chan error, 1)
	go func() {
		request, requestErr := http.NewRequest(http.MethodDelete, server.URL+"/api/sessions/"+created.ID+"?wait=1", nil)
		if requestErr != nil {
			deleteDone <- requestErr
			return
		}
		deleted, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			defer deleted.Body.Close()
			if deleted.StatusCode != http.StatusNoContent {
				requestErr = fmt.Errorf("delete status=%d", deleted.StatusCode)
			}
		}
		deleteDone <- requestErr
	}()

	select {
	case <-peerSawEOF:
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe the graceful TCP FIN")
	}
	select {
	case err := <-deleteDone:
		t.Fatalf("graceful delete returned before the 2.5 peer closed: %v", err)
	default:
	}
	release()
	releasedAt := time.Now()
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(releasedAt); elapsed < defaultLegacyLogoutSaveDrain-50*time.Millisecond {
			t.Fatalf("graceful delete returned before the legacy SAAC save drain: %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("graceful delete did not finish after peer close")
	}
}

func TestHTTPSessionPollCancellationCleansSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte{'L', 0})
		_, _ = io.Copy(io.Discard, connection)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("poll-cancellation TCP peer did not stop")
		}
	})

	handler, err := NewHandler(testConfig(listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pollRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/sessions/"+created.ID+"/events?timeout=60000", nil)
	if err != nil {
		t.Fatal(err)
	}
	pollDone := make(chan error, 1)
	go func() {
		pollResponse, requestErr := http.DefaultClient.Do(pollRequest)
		if pollResponse != nil {
			pollResponse.Body.Close()
		}
		pollDone <- requestErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		session, ok := handler.sessions.get(created.ID)
		if ok {
			session.mu.Lock()
			polling := session.polling
			session.mu.Unlock()
			if polling {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("events request did not reach the session poll")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("canceled events request did not return")
	}

	deadline = time.Now().Add(time.Second)
	for {
		response, err = http.Get(server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		var health map[string]any
		if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if health["sessions"] == float64(0) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("canceled poll leaked sessions=%v", health["sessions"])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTCPSessionGracefulCloseReportsPeerTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	defer peer.Close()

	session := newTCPSession("peer-timeout", client, 1024)
	err = session.closeGracefully(25 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("graceful close error=%v", err)
	}
	if !session.isClosed() {
		t.Fatal("timed-out graceful close left the bridge session open")
	}
}

func TestHTTPSessionCloseAfterPacketIsAtomic(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}

	packet := []byte("logout-packet\n")
	encoded, _ := json.Marshal(sendRequest{Packet: base64.StdEncoding.EncodeToString(packet)})
	response, err = http.Post(server.URL+"/api/sessions/"+created.ID+"/send?close=1", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("close-after send status=%d", response.StatusCode)
	}
	select {
	case received := <-fake.received:
		if !bytes.Equal(received, packet) {
			t.Fatalf("upstream received=%q want=%q", received, packet)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive close-after packet")
	}
	response, err = http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if health["sessions"] != float64(0) {
		t.Fatalf("close-after left sessions=%v", health["sessions"])
	}
}

func TestHTTPSessionCloseAfterPacketFailureStillCleansSession(t *testing.T) {
	fake := newGreetingThenCloseTCP(t, []byte{'L', 0})
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	select {
	case <-fake.accepted:
	case <-time.After(time.Second):
		t.Fatal("fake TCP server did not accept")
	}

	packet := []byte("logout-packet\n")
	encoded, _ := json.Marshal(sendRequest{Packet: base64.StdEncoding.EncodeToString(packet)})
	response, err = http.Post(server.URL+"/api/sessions/"+created.ID+"/send?close=1", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusAccepted {
		t.Fatal("close-after write unexpectedly succeeded after upstream close")
	}

	deadline := time.Now().Add(time.Second)
	for {
		response, err = http.Get(server.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		var health map[string]any
		if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if health["sessions"] == float64(0) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("close-after failure leaked sessions=%v", health["sessions"])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandlerRejectsInvalidGreetingAndUnknownSession(t *testing.T) {
	fake := newFakeTCP(t, []byte("NO"), nil)
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("invalid greeting status=%d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/api/sessions/no-such/events")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status=%d", response.StatusCode)
	}
}

func TestHandlerValidatesConfigAndPacketFraming(t *testing.T) {
	if _, err := NewHandler(Config{TCPUpstream: "not-an-address"}); err == nil {
		t.Fatal("invalid upstream address accepted")
	}
	left, right := net.Pipe()
	session := newTCPSession("test", left, 64)
	defer right.Close()
	if err := session.write([]byte("missing delimiter")); err == nil {
		t.Fatal("packet without newline accepted")
	}
	readDone := make(chan struct{})
	go func() {
		_, _ = io.ReadFull(right, make([]byte, 3))
		close(readDone)
	}()
	if err := session.write([]byte("ok\n")); err != nil {
		t.Fatal(err)
	}
	<-readDone
}

func TestSessionAllowsOnlyOneConcurrentPoll(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("test", left, 64)
	defer right.Close()
	result := make(chan error, 1)
	go func() {
		_, err := session.poll(time.Second)
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		polling := session.polling
		session.mu.Unlock()
		if polling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first poll did not become pending")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := session.poll(time.Millisecond); err == nil {
		t.Fatal("concurrent poll unexpectedly accepted")
	}
	session.finish("test complete")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestHandlerLimitsSessionsAndPackets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer func() {
		close(stop)
		_ = listener.Close()
	}()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = connection.Write([]byte{'L', 0})
			go func() {
				select {
				case <-stop:
					_ = connection.Close()
				}
			}()
		}
	}()
	cfg := testConfig(listener.Addr().String())
	cfg.MaxSessions = 1
	cfg.PacketLimit = 8
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("first create status=%d", response.StatusCode)
	}
	response, err = http.Post(server.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second create status=%d", response.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/sessions/"+created.ID+"/send", strings.NewReader("too-long\n"))
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("oversize send status=%d", response.StatusCode)
	}
}
