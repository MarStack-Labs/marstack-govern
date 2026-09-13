package audit

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const headFile = "chain-head"

type archived struct {
	AuditID  string `json:"auditID"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
	Event    Event  `json:"event"`
}

type FileArchive struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

func NewFileArchive(dir string) (*FileArchive, error) {
	if dir == "" {
		return nil, nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create the audit archive at %s: %w", dir, err)
	}

	return &FileArchive{dir: dir, now: time.Now}, nil
}

func (a *FileArchive) Write(_ context.Context, events []Event) error {
	if a == nil || len(events) == 0 {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	segment := filepath.Join(a.dir, fmt.Sprintf("audit-%s.jsonl", a.now().UTC().Format(time.DateOnly)))

	file, err := os.OpenFile(segment, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open audit segment %s: %w", segment, err)
	}
	defer func() { _ = file.Close() }()

	writer := bufio.NewWriter(file)

	for _, event := range events {
		line, err := json.Marshal(archived{
			AuditID:  event.AuditID,
			PrevHash: hex.EncodeToString(event.PrevHash),
			Hash:     hex.EncodeToString(event.Hash),
			Event:    event,
		})
		if err != nil {
			return fmt.Errorf("encode archived event %s: %w", event.AuditID, err)
		}

		if _, err := writer.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write archived event %s: %w", event.AuditID, err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush audit segment: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync audit segment: %w", err)
	}

	return a.writeHead(events[len(events)-1].Hash)
}

func (a *FileArchive) writeHead(hash []byte) error {
	path := filepath.Join(a.dir, headFile)

	if err := os.WriteFile(path, []byte(hex.EncodeToString(hash)), 0o640); err != nil {
		return fmt.Errorf("write the chain head: %w", err)
	}

	return nil
}

func (a *FileArchive) Head() ([]byte, error) {
	if a == nil {
		return nil, nil
	}

	raw, err := os.ReadFile(filepath.Join(a.dir, headFile))
	if os.IsNotExist(err) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the chain head: %w", err)
	}

	decoded, err := hex.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("the chain head is not a hash: %w", err)
	}

	return decoded, nil
}

func (a *FileArchive) Hashes() (map[string]string, error) {
	if a == nil {
		return nil, nil
	}

	entries, err := filepath.Glob(filepath.Join(a.dir, "audit-*.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("list audit segments: %w", err)
	}

	hashes := map[string]string{}

	for _, entry := range entries {
		file, err := os.Open(entry)
		if err != nil {
			return nil, fmt.Errorf("open audit segment %s: %w", entry, err)
		}

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)

		for scanner.Scan() {
			var record archived
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("read audit segment %s: %w", entry, err)
			}
			hashes[record.AuditID] = record.Hash
		}

		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("scan audit segment %s: %w", entry, err)
		}

		_ = file.Close()
	}

	return hashes, nil
}
