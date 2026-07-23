package manifest

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/model"
	"github.com/nextexcite/photo-bridge/internal/process"
)

// DefaultMetadataMemoryLimit is the largest metadata working set retained
// before sorted chunks are spilled. It applies to metadata only; it is not an
// rclone transfer buffer or a process RSS cap.
const DefaultMetadataMemoryLimit int64 = 64 << 20

type Builder struct {
	Executor   process.Executor
	RcloneBin  string
	RcloneConf string

	// TempDir is the parent directory for per-build temporary sort chunks. An
	// empty value uses the system temporary directory. Every build removes its
	// own child directory on success, failure, and cancellation.
	TempDir string

	// MetadataMemoryLimit controls the approximate in-memory encoded metadata
	// limit. Zero selects DefaultMetadataMemoryLimit.
	MetadataMemoryLimit int64
}

// Summary describes a manifest without retaining its entries.
type Summary struct {
	Level          string
	Entries        int
	Bytes          int64
	Fingerprint    string
	TemporaryBytes int64
}

// Result is retained for callers which need manifest entries in memory. New
// runtime code should use BuildToJSONL so large listings remain bounded.
type Result struct {
	Entries []model.ManifestEntry
	Summary
}

func (b Builder) Build(ctx context.Context, endpoint config.Endpoint, requested string) (Result, error) {
	return b.BuildFiltered(ctx, endpoint, requested, nil)
}

// BuildFiltered returns an in-memory sorted manifest for small, explicitly
// bounded control-plane selections. Runtime archival manifests use
// BuildToJSONL so whole libraries do not reside in memory.
func (b Builder) BuildFiltered(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}) (Result, error) {
	entries := make([]model.ManifestEntry, 0)
	summary, err := b.buildSorted(ctx, endpoint, requested, allowed, func(entry model.ManifestEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Entries: entries, Summary: summary}, nil
}

// BuildToJSONL writes a deterministic, atomically-published JSONL manifest.
// It streams the sorted manifest directly to destinationPath and therefore
// does not retain the complete listing in memory.
func (b Builder) BuildToJSONL(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}, destinationPath string) (Summary, error) {
	writer, publish, discard, err := createAtomicJSONL(destinationPath)
	if err != nil {
		return Summary{}, err
	}
	defer discard()

	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	summary, err := b.buildSorted(ctx, endpoint, requested, allowed, func(entry model.ManifestEntry) error {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("encode manifest entry: %w", err)
		}
		return nil
	})
	if err != nil {
		return Summary{}, err
	}
	if err := writer.Flush(); err != nil {
		return Summary{}, fmt.Errorf("flush manifest: %w", err)
	}
	if err := publish(); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (b Builder) buildSorted(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}, consume func(model.ManifestEntry) error) (Summary, error) {
	sorter, err := newSorter(b.TempDir, b.metadataMemoryLimit())
	if err != nil {
		return Summary{}, err
	}
	defer sorter.cleanup()

	level, err := b.collect(ctx, endpoint, requested, allowed, sorter.Add)
	if err != nil {
		return Summary{}, err
	}

	fingerprint := sha256.New()
	summary := Summary{Level: level}
	err = sorter.Drain(ctx, func(entry model.ManifestEntry) error {
		if err := writeFingerprintEntry(fingerprint, entry); err != nil {
			return err
		}
		summary.Entries++
		summary.Bytes += entry.Size
		return consume(entry)
	})
	summary.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
	summary.TemporaryBytes = sorter.temporaryBytes
	if err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (b Builder) metadataMemoryLimit() int64 {
	if b.MetadataMemoryLimit > 0 {
		return b.MetadataMemoryLimit
	}
	return DefaultMetadataMemoryLimit
}

func (b Builder) collect(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}, add func(model.ManifestEntry) error) (string, error) {
	switch endpoint.Driver {
	case "filesystem":
		return collectFilesystem(ctx, endpoint.Path, requested, allowed, add)
	case "rclone":
		return b.collectRclone(ctx, endpoint, requested, allowed, add)
	default:
		return "", fmt.Errorf("manifest driver %q is not supported", endpoint.Driver)
	}
}

// collectFilesystem hashes files in WalkDir order. This is deliberately
// sequential: parallel reads can make NAS and HDD-backed sources slower.
func collectFilesystem(ctx context.Context, root, requested string, allowed map[string]struct{}, add func(model.ManifestEntry) error) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect source root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("source filesystem path must be a directory")
	}

	level := "metadata"
	if requested == "sha256" || requested == "auto" {
		level = "sha256"
	}
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains unsupported symbolic link %q", relativePath(root, filePath))
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source contains unsupported non-regular file %q", relativePath(root, filePath))
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		relative := relativePath(root, filePath)
		if allowed != nil {
			if _, ok := allowed[relative]; !ok {
				return nil
			}
		}
		manifestEntry := model.ManifestEntry{Path: relative, Size: entryInfo.Size(), ModTime: entryInfo.ModTime().UTC()}
		if requested == "sha256" || requested == "auto" {
			hash, err := hashFile(ctx, filePath)
			if err != nil {
				return fmt.Errorf("hash %q: %w", manifestEntry.Path, err)
			}
			manifestEntry.HashAlgorithm = "sha256"
			manifestEntry.Hash = hash
		}
		return add(manifestEntry)
	})
	if err != nil {
		return "", err
	}
	return level, nil
}

type rcloneEntry struct {
	Path    string            `json:"Path"`
	Size    int64             `json:"Size"`
	ModTime time.Time         `json:"ModTime"`
	Hashes  map[string]string `json:"Hashes"`
	IsDir   bool              `json:"IsDir"`
}

func (b Builder) collectRclone(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}, add func(model.ManifestEntry) error) (string, error) {
	if b.Executor == nil {
		return "", errors.New("rclone manifest executor is not configured")
	}
	bin := b.RcloneBin
	if bin == "" {
		bin = "rclone"
	}
	args := []string{"lsjson", endpoint.RcloneSpec(), "--recursive", "--files-only", "--hash"}
	if b.RcloneConf != "" {
		args = append(args, "--config", b.RcloneConf)
	}

	reader, stdout := io.Pipe()
	var stderr bytes.Buffer
	type execution struct{ result process.Result }
	done := make(chan execution, 1)
	go func() {
		result := b.Executor.Run(ctx, bin, args, stdout, &stderr)
		if result.Err != nil {
			_ = stdout.CloseWithError(result.Err)
		} else {
			_ = stdout.Close()
		}
		done <- execution{result: result}
	}()

	level := "metadata"
	decodeErr := decodeRcloneArray(ctx, reader, func(remoteEntry rcloneEntry) error {
		if remoteEntry.IsDir {
			return nil
		}
		entryPath := filepath.ToSlash(remoteEntry.Path)
		if allowed != nil {
			if _, ok := allowed[entryPath]; !ok {
				return nil
			}
		}
		entry := model.ManifestEntry{Path: entryPath, Size: remoteEntry.Size, ModTime: remoteEntry.ModTime.UTC()}
		algorithm, hash := chooseHash(remoteEntry.Hashes, requested)
		if requested == "sha256" && algorithm != "sha256" {
			return fmt.Errorf("rclone source does not expose sha256 for %q; use manifest auto or metadata", entry.Path)
		}
		if hash != "" {
			entry.HashAlgorithm = algorithm
			entry.Hash = hash
			if algorithm == "sha256" {
				level = "sha256"
			} else if level != "sha256" {
				level = "provider-hash"
			}
		}
		return add(entry)
	})
	if decodeErr != nil {
		_ = reader.Close()
	}
	executionResult := <-done
	if executionResult.result.Err != nil {
		return "", fmt.Errorf("list rclone source: exit %d: %s", executionResult.result.ExitCode, strings.TrimSpace(stderr.String()))
	}
	if decodeErr != nil {
		return "", fmt.Errorf("decode rclone source listing: %w", decodeErr)
	}
	return level, nil
}

func decodeRcloneArray(ctx context.Context, reader io.Reader, consume func(rcloneEntry) error) error {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return errors.New("rclone listing is not a JSON array")
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var entry rcloneEntry
		if err := decoder.Decode(&entry); err != nil {
			return err
		}
		if err := consume(entry); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return errors.New("rclone listing has an invalid JSON array terminator")
	}
	return nil
}

func chooseHash(hashes map[string]string, requested string) (string, string) {
	if requested == "metadata" || len(hashes) == 0 {
		return "", ""
	}
	for key, value := range hashes {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", ""))
		if normalized == "sha256" && value != "" {
			return "sha256", value
		}
	}
	if requested == "sha256" {
		return "", ""
	}
	keys := make([]string, 0, len(hashes))
	for key, value := range hashes {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", ""
	}
	key := keys[0]
	return strings.ToLower(key), hashes[key]
}

// WalkJSONL streams a persisted deterministic manifest. It rejects malformed
// records and is the preferred API for verification and identity checks.
func WalkJSONL(ctx context.Context, filePath string, consume func(model.ManifestEntry) error) (Summary, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return Summary{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(bufio.NewReader(file))
	fingerprint := sha256.New()
	summary := Summary{}
	for {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		var entry model.ManifestEntry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			summary.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
			return summary, nil
		}
		if err != nil {
			return Summary{}, fmt.Errorf("decode manifest: %w", err)
		}
		if err := writeFingerprintEntry(fingerprint, entry); err != nil {
			return Summary{}, err
		}
		summary.Entries++
		summary.Bytes += entry.Size
		if err := consume(entry); err != nil {
			return Summary{}, err
		}
	}
}

// ReadJSONL is a compatibility helper for small manifests. New runtime code
// should use WalkJSONL to avoid materializing complete listings.
func ReadJSONL(filePath string) ([]model.ManifestEntry, error) {
	entries := make([]model.ManifestEntry, 0)
	_, err := WalkJSONL(context.Background(), filePath, func(entry model.ManifestEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// SummarizeJSONL streams a persisted manifest to derive the same stable
// identity used during construction without retaining its complete library.
func SummarizeJSONL(filePath string) (Summary, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return Summary{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(bufio.NewReader(file))
	fingerprint := sha256.New()
	summary := Summary{}
	for {
		var entry model.ManifestEntry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			summary.Fingerprint = hex.EncodeToString(fingerprint.Sum(nil))
			return summary, nil
		}
		if err != nil {
			return Summary{}, fmt.Errorf("decode manifest: %w", err)
		}
		if err := writeFingerprintEntry(fingerprint, entry); err != nil {
			return Summary{}, err
		}
		summary.Entries++
		summary.Bytes += entry.Size
	}
}

func WriteJSONL(filePath string, entries []model.ManifestEntry) error {
	writer, publish, discard, err := createAtomicJSONL(filePath)
	if err != nil {
		return err
	}
	defer discard()
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("encode manifest entry: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush manifest: %w", err)
	}
	return publish()
}

func createAtomicJSONL(filePath string) (*bufio.Writer, func() error, func(), error) {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return nil, nil, nil, fmt.Errorf("create manifest directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".manifest-*.tmp")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create temporary manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	discard := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o640); err != nil {
		discard()
		return nil, nil, nil, err
	}
	writer := bufio.NewWriter(temporary)
	publish := func() error {
		if err := temporary.Sync(); err != nil {
			return err
		}
		if err := temporary.Close(); err != nil {
			return err
		}
		if err := os.Rename(temporaryPath, filePath); err != nil {
			return fmt.Errorf("publish manifest: %w", err)
		}
		return nil
	}
	return writer, publish, discard, nil
}

func writeFingerprintEntry(hash io.Writer, entry model.ManifestEntry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode manifest fingerprint: %w", err)
	}
	if _, err := hash.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write manifest fingerprint: %w", err)
	}
	return nil
}

func hashFile(ctx context.Context, filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func relativePath(root, filePath string) string {
	relative, err := filepath.Rel(root, filePath)
	if err != nil {
		return filepath.ToSlash(filePath)
	}
	return filepath.ToSlash(relative)
}

type sorter struct {
	root           string
	limit          int64
	entries        []model.ManifestEntry
	entryBytes     int64
	chunks         []string
	temporaryBytes int64
}

func newSorter(parent string, limit int64) (*sorter, error) {
	if limit <= 0 {
		limit = DefaultMetadataMemoryLimit
	}
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, fmt.Errorf("create manifest temporary parent: %w", err)
	}
	root, err := os.MkdirTemp(parent, ".manifest-sort-")
	if err != nil {
		return nil, fmt.Errorf("create manifest temporary directory: %w", err)
	}
	if err := os.Chmod(root, 0o750); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &sorter{root: root, limit: limit}, nil
}

func (s *sorter) Add(entry model.ManifestEntry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode manifest entry for sort: %w", err)
	}
	entrySize := int64(len(encoded) + 1)
	if len(s.entries) > 0 && s.entryBytes+entrySize > s.limit {
		if err := s.flush(); err != nil {
			return err
		}
	}
	s.entries = append(s.entries, entry)
	s.entryBytes += entrySize
	return nil
}

func (s *sorter) Drain(ctx context.Context, consume func(model.ManifestEntry) error) error {
	if len(s.chunks) == 0 {
		sortEntries(s.entries)
		for _, entry := range s.entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(entry); err != nil {
				return err
			}
		}
		return nil
	}
	if len(s.entries) > 0 {
		if err := s.flush(); err != nil {
			return err
		}
	}
	return s.merge(ctx, consume)
}

func (s *sorter) flush() error {
	if len(s.entries) == 0 {
		return nil
	}
	sortEntries(s.entries)
	file, err := os.CreateTemp(s.root, "chunk-*.jsonl")
	if err != nil {
		return fmt.Errorf("create manifest sort chunk: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, entry := range s.entries {
		if err := encoder.Encode(entry); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return fmt.Errorf("encode manifest sort chunk: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	s.temporaryBytes += info.Size()
	s.chunks = append(s.chunks, path)
	s.entries = nil
	s.entryBytes = 0
	return nil
}

func (s *sorter) merge(ctx context.Context, consume func(model.ManifestEntry) error) error {
	readers := make([]*chunkReader, 0, len(s.chunks))
	defer func() {
		for _, reader := range readers {
			_ = reader.Close()
		}
	}()
	queue := make(entryHeap, 0, len(s.chunks))
	for index, path := range s.chunks {
		reader, err := openChunk(path)
		if err != nil {
			return err
		}
		readers = append(readers, reader)
		entry, ok, err := reader.Next()
		if err != nil {
			return err
		}
		if ok {
			queue = append(queue, heapEntry{entry: entry, reader: index})
		}
	}
	heap.Init(&queue)
	for queue.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		next := heap.Pop(&queue).(heapEntry)
		if err := consume(next.entry); err != nil {
			return err
		}
		entry, ok, err := readers[next.reader].Next()
		if err != nil {
			return err
		}
		if ok {
			heap.Push(&queue, heapEntry{entry: entry, reader: next.reader})
		}
	}
	return nil
}

func (s *sorter) cleanup() { _ = os.RemoveAll(s.root) }

func sortEntries(entries []model.ManifestEntry) {
	sort.Slice(entries, func(i, j int) bool { return compareEntries(entries[i], entries[j]) < 0 })
}

func compareEntries(left, right model.ManifestEntry) int {
	if left.Path != right.Path {
		return strings.Compare(left.Path, right.Path)
	}
	if left.Size != right.Size {
		if left.Size < right.Size {
			return -1
		}
		return 1
	}
	if !left.ModTime.Equal(right.ModTime) {
		if left.ModTime.Before(right.ModTime) {
			return -1
		}
		return 1
	}
	if left.HashAlgorithm != right.HashAlgorithm {
		return strings.Compare(left.HashAlgorithm, right.HashAlgorithm)
	}
	return strings.Compare(left.Hash, right.Hash)
}

type chunkReader struct {
	file    *os.File
	decoder *json.Decoder
}

func openChunk(path string) (*chunkReader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &chunkReader{file: file, decoder: json.NewDecoder(bufio.NewReader(file))}, nil
}
func (r *chunkReader) Next() (model.ManifestEntry, bool, error) {
	var entry model.ManifestEntry
	err := r.decoder.Decode(&entry)
	if errors.Is(err, io.EOF) {
		return model.ManifestEntry{}, false, nil
	}
	if err != nil {
		return model.ManifestEntry{}, false, err
	}
	return entry, true, nil
}
func (r *chunkReader) Close() error { return r.file.Close() }

type heapEntry struct {
	entry  model.ManifestEntry
	reader int
}
type entryHeap []heapEntry

func (h entryHeap) Len() int           { return len(h) }
func (h entryHeap) Less(i, j int) bool { return compareEntries(h[i].entry, h[j].entry) < 0 }
func (h entryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(value any)    { *h = append(*h, value.(heapEntry)) }
func (h *entryHeap) Pop() any          { old := *h; item := old[len(old)-1]; *h = old[:len(old)-1]; return item }
