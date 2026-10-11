package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// partialSize is how much of each file the quick check reads.
const partialSize = 64 * 1024

var errCancelled = errors.New("scan cancelled")

// FileEntry is one file inside a duplicate group.
type FileEntry struct {
	Path     string `json:"path"`
	Modified int64  `json:"modified"` // unix seconds
	size     int64
	modNano  int64
}

// Group is a set of files with identical content.
type Group struct {
	ID    int         `json:"id"`
	Size  int64       `json:"size"`
	Files []FileEntry `json:"files"`
}

// Status is what the UI polls while a scan runs.
type Status struct {
	Phase     string `json:"phase"` // idle, walking, hashing, done, error, cancelled
	Stage     string `json:"stage"`
	Root      string `json:"root"`
	Done      int64  `json:"done"`
	Total     int64  `json:"total"`
	FilesSeen int64  `json:"filesSeen"`
	Skipped   int64  `json:"skipped"`
	ElapsedMs int64  `json:"elapsedMs"`
	Error     string `json:"error,omitempty"`
}

// DeleteResult reports what happened to one requested file.
type DeleteResult struct {
	Path  string `json:"path"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Scanner struct {
	// Accessed atomically. Keep the int64 fields first so they stay aligned.
	done, total, filesSeen, skipped int64
	cancelFlag                      int32

	mu      sync.Mutex
	running bool
	phase   string
	stage   string
	root    string
	started time.Time
	ended   time.Time
	errMsg  string
	groups  []Group
}

func NewScanner() *Scanner { return &Scanner{phase: "idle"} }

func (s *Scanner) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Scanner) Start(root string, minSize int64, excludes []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return errors.New("A scan is already running.")
	}
	s.running = true
	s.phase = "walking"
	s.stage = "Looking for files"
	s.root = root
	s.started = time.Now()
	s.ended = time.Time{}
	s.errMsg = ""
	s.groups = nil
	atomic.StoreInt64(&s.done, 0)
	atomic.StoreInt64(&s.total, 0)
	atomic.StoreInt64(&s.filesSeen, 0)
	atomic.StoreInt64(&s.skipped, 0)
	atomic.StoreInt32(&s.cancelFlag, 0)
	go s.run(root, minSize, excludes)
	return nil
}

func (s *Scanner) Cancel() { atomic.StoreInt32(&s.cancelFlag, 1) }

func (s *Scanner) cancelled() bool { return atomic.LoadInt32(&s.cancelFlag) == 1 }

func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := time.Now()
	if !s.running && !s.ended.IsZero() {
		end = s.ended
	}
	var elapsed int64
	if !s.started.IsZero() {
		elapsed = end.Sub(s.started).Milliseconds()
	}
	return Status{
		Phase:     s.phase,
		Stage:     s.stage,
		Root:      s.root,
		Done:      atomic.LoadInt64(&s.done),
		Total:     atomic.LoadInt64(&s.total),
		FilesSeen: atomic.LoadInt64(&s.filesSeen),
		Skipped:   atomic.LoadInt64(&s.skipped),
		ElapsedMs: elapsed,
		Error:     s.errMsg,
	}
}

// Results returns a copy of the current duplicate groups.
func (s *Scanner) Results() []Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Group, len(s.groups))
	for i, g := range s.groups {
		ng := g
		ng.Files = append([]FileEntry(nil), g.Files...)
		out[i] = ng
	}
	return out
}

func (s *Scanner) setStage(stage string, total int64) {
	s.mu.Lock()
	s.phase = "hashing"
	s.stage = stage
	s.mu.Unlock()
	atomic.StoreInt64(&s.done, 0)
	atomic.StoreInt64(&s.total, total)
}

func (s *Scanner) run(root string, minSize int64, excludes []string) {
	groups, err := s.scan(root, minSize, excludes)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.ended = time.Now()
	switch {
	case errors.Is(err, errCancelled):
		s.phase = "cancelled"
	case err != nil:
		s.phase = "error"
		s.errMsg = err.Error()
	default:
		s.phase = "done"
		s.groups = groups
	}
}

type sizeHash struct {
	size int64
	hash string
}

func (s *Scanner) scan(root string, minSize int64, excludes []string) ([]Group, error) {
	skipPaths := systemSkipPaths()
	bySize := make(map[int64][]FileEntry)

	// Step 1: list files. Only files that share a size can be duplicates.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if s.cancelled() {
			return errCancelled
		}
		if err != nil {
			atomic.AddInt64(&s.skipped, 1)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && (shouldSkipDir(path, d.Name(), skipPaths) || isExcluded(path, excludes)) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // skips symlinks, devices, and so on
			return nil
		}
		info, err := d.Info()
		if err != nil {
			atomic.AddInt64(&s.skipped, 1)
			return nil
		}
		size := info.Size()
		if size == 0 || size < minSize {
			return nil
		}
		atomic.AddInt64(&s.filesSeen, 1)
		bySize[size] = append(bySize[size], FileEntry{
			Path:     path,
			Modified: info.ModTime().Unix(),
			size:     size,
			modNano:  info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	var stage1 []FileEntry
	for _, files := range bySize {
		if len(files) > 1 {
			stage1 = append(stage1, files...)
		}
	}
	if len(stage1) == 0 {
		return []Group{}, nil
	}

	// Step 2: quick check. Hash only the first 64 KB of each candidate.
	s.setStage("Quick check", int64(len(stage1)))
	res, err := s.hashAll(stage1, partialSize)
	if err != nil {
		return nil, err
	}
	byPartial := make(map[sizeHash][]FileEntry)
	for _, r := range res {
		if !r.ok {
			atomic.AddInt64(&s.skipped, 1)
			continue
		}
		k := sizeHash{r.f.size, r.h}
		byPartial[k] = append(byPartial[k], r.f)
	}

	var final [][]FileEntry
	var stage2 []FileEntry
	for k, files := range byPartial {
		if len(files) < 2 {
			continue
		}
		if k.size <= partialSize {
			final = append(final, files) // the quick hash already covered the whole file
		} else {
			stage2 = append(stage2, files...)
		}
	}

	// Step 3: full check. Hash the whole file, but only for survivors.
	if len(stage2) > 0 {
		s.setStage("Full check", int64(len(stage2)))
		res, err := s.hashAll(stage2, 0)
		if err != nil {
			return nil, err
		}
		byFull := make(map[sizeHash][]FileEntry)
		for _, r := range res {
			if !r.ok {
				atomic.AddInt64(&s.skipped, 1)
				continue
			}
			k := sizeHash{r.f.size, r.h}
			byFull[k] = append(byFull[k], r.f)
		}
		for _, files := range byFull {
			if len(files) > 1 {
				final = append(final, files)
			}
		}
	}

	groups := make([]Group, 0, len(final))
	for _, files := range final {
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		groups = append(groups, Group{Size: files[0].size, Files: files})
	}
	sort.Slice(groups, func(i, j int) bool {
		wi := groups[i].Size * int64(len(groups[i].Files)-1)
		wj := groups[j].Size * int64(len(groups[j].Files)-1)
		if wi != wj {
			return wi > wj
		}
		return groups[i].Files[0].Path < groups[j].Files[0].Path
	})
	for i := range groups {
		groups[i].ID = i
	}
	return groups, nil
}

type hashed struct {
	f  FileEntry
	h  string
	ok bool
}

// hashAll hashes files in parallel. limit > 0 hashes only the first limit bytes.
func (s *Scanner) hashAll(files []FileEntry, limit int64) ([]hashed, error) {
	out := make([]hashed, len(files))
	workers := runtime.NumCPU()
	if workers > 4 { // more threads just makes a spinning disk seek
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}
	var next int64 = -1
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 256*1024)
			for {
				i := int(atomic.AddInt64(&next, 1))
				if i >= len(files) || s.cancelled() {
					return
				}
				h, err := hashFile(files[i].Path, limit, buf)
				out[i] = hashed{f: files[i], h: h, ok: err == nil}
				atomic.AddInt64(&s.done, 1)
			}
		}()
	}
	wg.Wait()
	if s.cancelled() {
		return nil, errCancelled
	}
	return out, nil
}

func hashFile(path string, limit int64, buf []byte) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	var r io.Reader = f
	if limit > 0 {
		r = io.LimitReader(f, limit)
	}
	if _, err := io.CopyBuffer(h, r, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Folder names skipped wherever they appear. Identical files in these places
// are normal, and removing them can break programs, installers or projects.
var skipAnywhere = map[string]bool{
	".git": true, "node_modules": true, "$recycle.bin": true,
	"system volume information": true, "dupefinder-trash": true,
	"drivers": true, "driver": true, "driverstore": true, "swsetup": true,
}

// Folder names skipped only when they sit directly in a drive root,
// such as C:\Intel or C:\Dell. Manufacturers leave driver and recovery
// files there.
var skipAtRoot = map[string]bool{
	"intel": true, "amd": true, "nvidia": true, "dell": true, "hp": true, "lenovo": true,
	"perflogs": true, "recovery": true, "windows.old": true, "msocache": true,
	"config.msi": true, "$sysreset": true, "$windows.~bt": true, "$windows.~ws": true,
}

// shouldSkipDir keeps the scan out of places where deleting "duplicates"
// can break programs or projects.
func shouldSkipDir(path, name string, skipPaths []string) bool {
	lower := strings.ToLower(name)
	if skipAnywhere[lower] || extraSkipNames[lower] {
		return true
	}
	if skipAtRoot[lower] && filepath.Dir(path) == filepath.VolumeName(path)+string(filepath.Separator) {
		return true
	}
	for _, p := range skipPaths {
		if strings.EqualFold(path, p) {
			return true
		}
	}
	return false
}

// isExcluded reports whether path is one of the folders the person chose to
// skip, or lives inside one. Excludes are already absolute and cleaned.
func isExcluded(path string, excludes []string) bool {
	for _, e := range excludes {
		if e == "" {
			continue
		}
		if strings.EqualFold(path, e) {
			return true
		}
		prefix := e
		if !os.IsPathSeparator(prefix[len(prefix)-1]) {
			prefix += string(os.PathSeparator)
		}
		if len(path) >= len(prefix) && strings.EqualFold(path[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// Delete moves the given files to the trash. It only touches files from the
// last scan, never removes the last intact copy in a group, and refuses files
// that changed since they were scanned.
func (s *Scanner) Delete(paths []string) []DeleteResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]DeleteResult, 0, len(paths))
	fail := func(p, msg string) {
		results = append(results, DeleteResult{Path: p, Error: msg})
	}

	if s.running {
		for _, p := range paths {
			fail(p, "A scan is still running.")
		}
		return results
	}

	type loc struct{ g, f int }
	index := make(map[string]loc)
	for gi, g := range s.groups {
		for fi, f := range g.Files {
			index[f.Path] = loc{gi, fi}
		}
	}

	// Files the caller wants to remove, per group.
	requested := make(map[int]map[string]bool)
	for _, p := range paths {
		if l, ok := index[p]; ok {
			if requested[l.g] == nil {
				requested[l.g] = make(map[string]bool)
			}
			requested[l.g][p] = true
		}
	}

	// For each group: is at least one file we are NOT removing still intact?
	safe := make(map[int]bool)
	for gi, req := range requested {
		g := s.groups[gi]
		for _, f := range g.Files {
			if req[f.Path] {
				continue
			}
			st, err := os.Lstat(f.Path)
			if err == nil && st.Mode().IsRegular() && st.Size() == g.Size && st.ModTime().UnixNano() == f.modNano {
				safe[gi] = true
				break
			}
		}
	}

	deleted := make(map[string]bool)
	for _, p := range paths {
		l, ok := index[p]
		if !ok {
			fail(p, "This file wasn't part of the last scan.")
			continue
		}
		if deleted[p] {
			continue
		}
		if !safe[l.g] {
			fail(p, "Skipped to keep at least one intact copy of this file.")
			continue
		}
		g := s.groups[l.g]
		f := g.Files[l.f]
		st, err := os.Lstat(f.Path)
		if err != nil {
			fail(p, "The file is already gone. Scan again to refresh the list.")
			continue
		}
		if !st.Mode().IsRegular() || st.Size() != g.Size || st.ModTime().UnixNano() != f.modNano {
			fail(p, "The file changed after the scan. Scan again to refresh the list.")
			continue
		}
		if err := moveToTrash(f.Path); err != nil {
			fail(p, err.Error())
			continue
		}
		deleted[p] = true
		results = append(results, DeleteResult{Path: p, OK: true})
	}

	if len(deleted) > 0 {
		kept := make([]Group, 0, len(s.groups))
		for _, g := range s.groups {
			var files []FileEntry
			for _, f := range g.Files {
				if !deleted[f.Path] {
					files = append(files, f)
				}
			}
			if len(files) >= 2 {
				g.Files = files
				g.ID = len(kept)
				kept = append(kept, g)
			}
		}
		s.groups = kept
	}
	return results
}
