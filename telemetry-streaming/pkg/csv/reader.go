package csv

import (
	"bufio"
	"context"
	"encoding/csv"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Record represents a single CSV row as key-value pairs
type Record struct {
	Headers []string
	Values  []string
	LineNum int64
}

// ToMap converts record to map
func (r *Record) ToMap() map[string]string {
	result := make(map[string]string)
	for i, header := range r.Headers {
		if i < len(r.Values) {
			result[header] = r.Values[i]
		}
	}
	return result
}

// Reader reads CSV file and streams records with configurable speed
type Reader struct {
	filePath  string
	readSpeed int  // records per second
	loopFile  bool // loop file reading continuously
	logger    *zap.Logger
	mu        sync.RWMutex
	closed    bool
}

// NewReader creates new CSV reader
func NewReader(filePath string, readSpeed int, logger *zap.Logger) *Reader {
	return &Reader{
		filePath:  filePath,
		readSpeed: readSpeed,
		loopFile:  false,
		logger:    logger,
		closed:    false,
	}
}

// NewReaderWithLoop creates new CSV reader with loop option
func NewReaderWithLoop(filePath string, readSpeed int, loopFile bool, logger *zap.Logger) *Reader {
	return &Reader{
		filePath:  filePath,
		readSpeed: readSpeed,
		loopFile:  loopFile,
		logger:    logger,
		closed:    false,
	}
}

// StreamRecords reads CSV and sends records to channel with rate limiting
func (r *Reader) StreamRecords(ctx context.Context) <-chan *Record {
	recordsChan := make(chan *Record, 100)

	go func() {
		defer close(recordsChan)

		loopCount := 0
		totalProcessed := int64(0)

		for {
			// Check if we should exit
			select {
			case <-ctx.Done():
				r.logger.Info("CSV stream cancelled",
					zap.Int64("total_records_processed", totalProcessed),
					zap.Int("loop_count", loopCount))
				return
			default:
			}

			r.logger.Debug("opening CSV file for reading",
				zap.String("path", r.filePath),
				zap.Int("loop_count", loopCount))

			file, err := os.Open(r.filePath)
			if err != nil {
				r.logger.Error("failed to open CSV file",
					zap.String("path", r.filePath),
					zap.Error(err))
				return
			}

			reader := csv.NewReader(bufio.NewReader(file))

			// Read headers
			headers, err := reader.Read()
			if err != nil {
				r.logger.Error("failed to read CSV headers", zap.Error(err))
				file.Close()
				return
			}

			if loopCount == 0 {
				r.logger.Info("CSV headers read", zap.Int("column_count", len(headers)))
			}

			// Calculate throttle duration
			throttleDuration := time.Duration(1000000000 / r.readSpeed) // nanoseconds
			ticker := time.NewTicker(throttleDuration)

			lineNum := int64(1)
			loopProcessed := int64(0)

			InnerLoop:
			for {
				select {
				case <-ctx.Done():
					r.logger.Info("CSV stream cancelled during processing",
						zap.Int64("total_records_processed", totalProcessed),
						zap.Int("loop_count", loopCount))
					ticker.Stop()
					file.Close()
					return
				case <-ticker.C:
					values, err := reader.Read()
					if err != nil {
						// EOF reached
						loopProcessed = lineNum - 1
						r.logger.Info("CSV file end reached",
							zap.Int("loop_count", loopCount),
							zap.Int64("records_in_loop", loopProcessed),
							zap.Int64("total_records", totalProcessed),
							zap.Bool("loop_file_enabled", r.loopFile))
						ticker.Stop()
						file.Close()

						if !r.loopFile {
							// Exit if looping is disabled
							r.logger.Info("CSV streaming completed (looping disabled)",
								zap.Int64("total_records", totalProcessed))
							return
						}

						// Loop back to start
						loopCount++
						r.logger.Info("Restarting CSV file from beginning",
							zap.Int("loop_count", loopCount),
							zap.Int64("total_records_so_far", totalProcessed))
						break InnerLoop // Break inner loop to restart outer loop
					}

					record := &Record{
						Headers: headers,
						Values:  values,
						LineNum: lineNum,
					}

					select {
					case recordsChan <- record:
						lineNum++
						totalProcessed++
					case <-ctx.Done():
						r.logger.Info("CSV stream cancelled during send",
							zap.Int64("total_records_processed", totalProcessed))
						ticker.Stop()
						file.Close()
						return
					}
				}
			}
		}
	}()

	return recordsChan
}

// Close closes the reader
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.logger.Debug("CSV reader closed")
	return nil
}

// IsClosed returns whether reader is closed
func (r *Reader) IsClosed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}
