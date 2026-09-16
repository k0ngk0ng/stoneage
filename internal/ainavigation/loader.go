package ainavigation

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Options selects the server data files used by Load. DataDir accepts the
// exact gmsv data directory or a repository/gmsv root containing data/map.
// MapDir and MapsetFile override the paths derived from DataDir and are useful
// for tests or a separately mounted server data volume.
type Options struct {
	DataDir      string
	MapDir       string
	MapsetFile   string
	MaxMaps      int
	MaxDimension int
	MaxCells     int
}

// DefaultMaxMaps is the same bound used by the legacy server's
// MAX_MAP_FILES. The checked-in 2.5 data has fewer files.
const DefaultMaxMaps = 1300

// Load parses map/mapset.txt and every recursive LS2MAP file below mapdir.
// It follows MAP_readMapDir's recursive file discovery but sorts paths so a
// duplicate floor ID has deterministic last-record-wins behavior. Such
// duplicate IDs exist in the historical data and are therefore retained
// rather than making the complete data set unloadable.
func Load(ctx context.Context, options Options) (*Navigator, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mapDir, mapsetFile, err := resolvePaths(options)
	if err != nil {
		return nil, err
	}
	mapset, err := os.Open(mapsetFile)
	if err != nil {
		return nil, fmt.Errorf("%w: open mapset %s: %v", ErrInvalidMap, mapsetFile, err)
	}
	images, parseErr := ParseMapset(mapset)
	closeErr := mapset.Close()
	if parseErr != nil {
		return nil, parseErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: close mapset %s: %v", ErrInvalidMap, mapsetFile, closeErr)
	}

	maxMaps := options.MaxMaps
	if maxMaps == 0 {
		maxMaps = DefaultMaxMaps
	}
	if maxMaps < 0 {
		return nil, fmt.Errorf("%w: negative max maps", ErrInvalidMap)
	}
	maxDimension := options.MaxDimension
	if maxDimension == 0 {
		maxDimension = maxMapDimension
	}
	if maxDimension <= 0 || maxDimension > maxMapDimension {
		return nil, fmt.Errorf("%w: max dimension must be between 1 and %d", ErrInvalidMap, maxMapDimension)
	}
	maxCells := options.MaxCells
	if maxCells == 0 {
		maxCells = maxMapCells
	}
	if maxCells <= 0 || maxCells > maxMapCells {
		return nil, fmt.Errorf("%w: max cells must be between 1 and %d", ErrInvalidMap, maxMapCells)
	}

	floors := make([]FloorMap, 0)
	err = filepath.WalkDir(mapDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		// A cheap six-byte probe avoids parsing mapset.txt, mapwarp.txt,
		// and NPC sidecar files as maps while preserving an error for a file
		// which advertises LS2MAP but is truncated.
		magic := make([]byte, len(ls2MapMagic))
		readCount, readErr := file.ReadAt(magic, 0)
		if readErr != nil || readCount != len(magic) {
			return file.Close()
		}
		if string(magic) != ls2MapMagic {
			return file.Close()
		}
		if maxMaps > 0 && len(floors) >= maxMaps {
			_ = file.Close()
			return fmt.Errorf("%w: more than %d LS2MAP files", ErrInvalidMap, maxMaps)
		}
		if _, seekErr := file.Seek(0, 0); seekErr != nil {
			_ = file.Close()
			return seekErr
		}
		floor, parseErr := parseLS2MAPWithLimits(file, images, maxDimension, maxCells)
		closeErr := file.Close()
		if parseErr != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidMap, path, parseErr)
		}
		if closeErr != nil {
			return closeErr
		}
		relative, relErr := filepath.Rel(mapDir, path)
		if relErr != nil {
			return relErr
		}
		floor.Source = filepath.ToSlash(relative)
		floors = append(floors, floor)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: walk map directory %s: %v", ErrInvalidMap, mapDir, err)
	}
	if len(floors) == 0 {
		return nil, fmt.Errorf("%w: no LS2MAP files below %s", ErrInvalidMap, mapDir)
	}
	return New(images, floors)
}

// LoadDataDir is the context-aware convenience form used by game services
// which already know the gmsv data directory.
func LoadDataDir(ctx context.Context, dataDir string) (*Navigator, error) {
	return Load(ctx, Options{DataDir: dataDir})
}

// LoadPath is the context-free convenience form for command-line tools.
func LoadPath(dataDir string) (*Navigator, error) {
	return Load(context.Background(), Options{DataDir: dataDir})
}

func resolvePaths(options Options) (string, string, error) {
	mapDir := strings.TrimSpace(options.MapDir)
	mapsetFile := strings.TrimSpace(options.MapsetFile)
	dataDir := strings.TrimSpace(options.DataDir)
	if mapDir != "" {
		mapDir = filepath.Clean(mapDir)
		if mapsetFile == "" {
			mapsetFile = filepath.Join(mapDir, "mapset.txt")
		} else if !filepath.IsAbs(mapsetFile) {
			mapsetFile = filepath.Join(mapDir, mapsetFile)
		}
		if !filepath.IsAbs(mapsetFile) {
			mapsetFile = filepath.Clean(mapsetFile)
		}
		if stat, err := os.Stat(mapDir); err != nil || !stat.IsDir() {
			return "", "", fmt.Errorf("%w: map directory %s is unavailable", ErrInvalidMap, mapDir)
		}
		return mapDir, mapsetFile, nil
	}
	if dataDir != "" {
		dataDir = filepath.Clean(dataDir)
		candidates := []string{dataDir, filepath.Join(dataDir, "data")}
		// Accept an already selected data/map directory as a convenience
		// for callers that mount only the map volume.
		if stat, statErr := os.Stat(filepath.Join(dataDir, "mapset.txt")); statErr == nil && !stat.IsDir() {
			if mapsetFile == "" {
				mapsetFile = filepath.Join(dataDir, "mapset.txt")
			} else if !filepath.IsAbs(mapsetFile) {
				mapsetFile = filepath.Join(dataDir, mapsetFile)
			}
			return dataDir, filepath.Clean(mapsetFile), nil
		}
		for _, candidate := range candidates {
			candidate = filepath.Clean(candidate)
			if stat, err := os.Stat(filepath.Join(candidate, "map")); err == nil && stat.IsDir() && fileExists(filepath.Join(candidate, "map", "mapset.txt")) {
				if mapsetFile == "" {
					mapsetFile = filepath.Join(candidate, "map", "mapset.txt")
				} else if !filepath.IsAbs(mapsetFile) {
					if filepath.Dir(mapsetFile) == "." {
						mapsetFile = filepath.Join(candidate, "map", mapsetFile)
					} else {
						mapsetFile = filepath.Join(candidate, mapsetFile)
					}
				}
				return filepath.Join(candidate, "map"), filepath.Clean(mapsetFile), nil
			}
		}
		return "", "", fmt.Errorf("%w: no data/map below %s", ErrInvalidMap, dataDir)
	}
	for _, candidate := range []string{
		filepath.Join("server", "legacy", "source", "2.5", "gmsv", "data"),
		filepath.Join("runtime", "legacy-server", "gmsv", "data"),
		filepath.Join("data"),
	} {
		mapDir = filepath.Join(candidate, "map")
		mapsetFile = filepath.Join(mapDir, "mapset.txt")
		if stat, err := os.Stat(mapDir); err == nil && stat.IsDir() && fileExists(mapsetFile) {
			return mapDir, mapsetFile, nil
		}
	}
	return "", "", fmt.Errorf("%w: could not locate a 2.5 data/map directory", ErrInvalidMap)
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}
