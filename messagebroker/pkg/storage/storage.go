package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MessageLog represents a log file for storing messages
type MessageLog struct {
	Topic       string
	PartitionID int32
	BaseOffset  int64
	Filepath    string
	CurrentSize int64
	MaxSize     int64
	Messages    []map[string]interface{}
	LastUpdated time.Time
	mu          sync.RWMutex
}

// IndexEntry represents an entry in the index
type IndexEntry struct {
	Offset    int64
	Position  int64
	Timestamp int64
}

// Index manages offset to file position mapping
type Index struct {
	Topic       string
	PartitionID int32
	Entries     []IndexEntry
	Filepath    string
	mu          sync.RWMutex
}

// Storage handles message persistence
type Storage struct {
	dataDir          string
	logs             map[string]*MessageLog // "topic-partition" -> MessageLog
	indices          map[string]*Index      // "topic-partition" -> Index
	segmentSizeBytes int64
	retentionMs      int64
	mu               sync.RWMutex
}

// NewStorage creates a new storage instance
func NewStorage(dataDir string, segmentSize int64, retentionMs int64) *Storage {
	return &Storage{
		dataDir:          dataDir,
		logs:             make(map[string]*MessageLog),
		indices:          make(map[string]*Index),
		segmentSizeBytes: segmentSize,
		retentionMs:      retentionMs,
	}
}

// CreatePartitionLog creates a new message log for a partition
func (s *Storage) CreatePartitionLog(topic string, partitionID int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := topic + "-" + string(rune(partitionID))

	// Create directory structure
	logDir := filepath.Join(s.dataDir, topic, string(rune(partitionID)))
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	// Create log file
	logFile := filepath.Join(logDir, "log.data")
	indexFile := filepath.Join(logDir, "index.idx")

	log := &MessageLog{
		Topic:       topic,
		PartitionID: partitionID,
		BaseOffset:  0,
		Filepath:    logFile,
		MaxSize:     s.segmentSizeBytes,
		CurrentSize: 0,
		Messages:    make([]map[string]interface{}, 0),
		LastUpdated: time.Now(),
	}

	index := &Index{
		Topic:       topic,
		PartitionID: partitionID,
		Entries:     make([]IndexEntry, 0),
		Filepath:    indexFile,
	}

	s.logs[key] = log
	s.indices[key] = index

	return nil
}

// WriteMessage writes a message to the log
func (s *Storage) WriteMessage(topic string, partitionID int32, offset int64, message map[string]interface{}) error {
	s.mu.RLock()
	key := topic + "-" + string(rune(partitionID))
	log := s.logs[key]
	s.mu.RUnlock()

	if log == nil {
		return ErrLogNotFound
	}

	log.mu.Lock()
	defer log.mu.Unlock()

	// Add message to log
	message["offset"] = offset
	message["timestamp"] = time.Now().UnixMilli()
	log.Messages = append(log.Messages, message)

	// Update index
	s.mu.Lock()
	index := s.indices[key]
	s.mu.Unlock()

	if index != nil {
		index.mu.Lock()
		index.Entries = append(index.Entries, IndexEntry{
			Offset:    offset,
			Position:  int64(len(log.Messages) - 1),
			Timestamp: time.Now().Unix(),
		})
		index.mu.Unlock()
	}

	data, err := json.Marshal(message)
	if err != nil {
		return err
	}

	// Check if we need to rotate log
	log.CurrentSize += int64(len(data))
	if log.CurrentSize >= log.MaxSize {
		// In production, would rotate to new segment
		s.rotateLog(topic, partitionID)
	}

	return nil
}

// ReadMessages reads messages from offset
func (s *Storage) ReadMessages(topic string, partitionID int32, offset int64, maxMessages int32) ([]map[string]interface{}, error) {
	s.mu.RLock()
	key := topic + "-" + string(rune(partitionID))
	log := s.logs[key]
	s.mu.RUnlock()

	if log == nil {
		return nil, ErrLogNotFound
	}

	log.mu.RLock()
	defer log.mu.RUnlock()

	messages := make([]map[string]interface{}, 0)
	count := 0

	for _, msg := range log.Messages {
		msgOffset := msg["offset"].(int64)
		if msgOffset >= offset && count < int(maxMessages) {
			messages = append(messages, msg)
			count++
		}
	}

	return messages, nil
}

// GetOffset retrieves the current offset for a partition
func (s *Storage) GetOffset(topic string, partitionID int32) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := topic + "-" + string(rune(partitionID))
	log, exists := s.logs[key]
	if !exists {
		return -1, ErrLogNotFound
	}

	log.mu.RLock()
	defer log.mu.RUnlock()

	if len(log.Messages) == 0 {
		return 0, nil
	}

	lastMsg := log.Messages[len(log.Messages)-1]
	return lastMsg["offset"].(int64), nil
}

// Flush flushes data to disk
func (s *Storage) Flush(topic string, partitionID int32) error {
	// In production, would persist to actual disk
	return nil
}

// Cleanup removes old messages based on retention
func (s *Storage) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	for _, log := range s.logs {
		log.mu.Lock()
		// Remove messages older than retention period
		newMessages := make([]map[string]interface{}, 0)

		for _, msg := range log.Messages {
			timestamp := msg["timestamp"].(int64)
			age := now.UnixMilli() - timestamp

			if age < s.retentionMs {
				newMessages = append(newMessages, msg)
			}
		}

		log.Messages = newMessages
		log.mu.Unlock()
	}

	return nil
}

// rotateLog rotates the current log file
func (s *Storage) rotateLog(topic string, partitionID int32) error {
	key := topic + "-" + string(rune(partitionID))

	s.mu.Lock()
	log := s.logs[key]
	s.mu.Unlock()

	if log == nil {
		return ErrLogNotFound
	}

	log.mu.Lock()
	defer log.mu.Unlock()

	// Create new log segment
	log.BaseOffset += int64(len(log.Messages))
	log.Messages = make([]map[string]interface{}, 0)
	log.CurrentSize = 0

	return nil
}

// Error definitions
var (
	ErrLogNotFound   = NewStorageError("log not found")
	ErrOffsetInvalid = NewStorageError("invalid offset")
)

// StorageError represents a storage error
type StorageError struct {
	Message string
}

func NewStorageError(msg string) *StorageError {
	return &StorageError{Message: msg}
}

func (e *StorageError) Error() string {
	return e.Message
}
