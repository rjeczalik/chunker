package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// JsonData represents the JSON structure for each chunk
type JsonData struct {
	Data string `json:"data"`
}

// loadGoldenFile loads the expected output from the golden file
func loadGoldenFile(filename string) ([]JsonData, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var chunks []JsonData
	decoder := json.NewDecoder(file)

	for {
		var chunk JsonData
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// generateChunks processes the WAV file and returns chunks
func generateChunks(filename string, chunkSize int) ([][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	chunker := NewWAVChunker(file)
	var chunks [][]byte

	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// BenchmarkWAVChunking benchmarks the chunking performance
func BenchmarkWAVChunking(b *testing.B) {
	// Reset timer to exclude setup time
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Open file for each iteration
		file, err := os.Open("sample.wav")
		if err != nil {
			b.Fatal(err)
		}

		chunker := NewWAVChunker(file)

		// Process all chunks
		for {
			_, err := chunker.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}

		file.Close()
	}
}

// TestWAVChunkingCorrectness tests that the chunks match the golden file
func TestWAVChunkingCorrectness(t *testing.T) {
	// Load golden file
	goldenChunks, err := loadGoldenFile("wav-complete-8192.json")
	if err != nil {
		t.Fatalf("Failed to load golden file: %v", err)
	}

	// Generate chunks
	actualChunks, err := generateChunks("sample.wav", 8192)
	if err != nil {
		t.Fatalf("Failed to generate chunks: %v", err)
	}

	// Compare lengths
	if len(actualChunks) != len(goldenChunks) {
		t.Fatalf("Chunk count mismatch: expected %d, got %d", len(goldenChunks), len(actualChunks))
	}

	// Compare each chunk
	for i, expectedChunk := range goldenChunks {
		expectedData, err := base64.StdEncoding.DecodeString(expectedChunk.Data)
		if err != nil {
			t.Fatalf("Failed to decode expected chunk %d: %v", i, err)
		}

		if !bytes.Equal(actualChunks[i], expectedData) {
			t.Errorf("Chunk %d mismatch: expected %d bytes, got %d bytes", i, len(expectedData), len(actualChunks[i]))
		}
	}
}

// BenchmarkWAVChunkingWithAllocations benchmarks with allocation reporting
func BenchmarkWAVChunkingWithAllocations(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		file, err := os.Open("sample.wav")
		if err != nil {
			b.Fatal(err)
		}

		chunker := NewWAVChunker(file)

		for {
			_, err := chunker.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}

		file.Close()
	}
}

// BenchmarkWAVChunkingConcurrent simulates concurrent chunking operations
// like in a service handling multiple requests
func BenchmarkWAVChunkingConcurrent(b *testing.B) {
	const numWorkers = 10

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			file, err := os.Open("sample.wav")
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()

			chunker := NewWAVChunker(file)

			// Process all chunks
			for {
				_, err := chunker.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}

// BenchmarkWAVChunkingHighConcurrency simulates high concurrent load
// like in a busy service with many simultaneous requests
func BenchmarkWAVChunkingHighConcurrency(b *testing.B) {
	b.ResetTimer()
	b.SetParallelism(50) // Increase parallelism to simulate high load
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			file, err := os.Open("sample.wav")
			if err != nil {
				b.Fatal(err)
			}
			defer file.Close()

			chunker := NewWAVChunker(file)

			// Process all chunks
			for {
				_, err := chunker.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
}
