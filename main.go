package main

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type DataChunk struct {
	Data string `json:"data"`
}

func main() {
	var blockSize int
	var fileType string
	var mode string
	var compressionLevel int
	flag.IntVar(&blockSize, "b", 8192, "block size for chunking")
	flag.StringVar(&fileType, "type", "auto", "file type: mp3, wav, dumb, or auto")
	flag.StringVar(&mode, "mode", "streaming", "chunking mode: streaming, complete (WAV only)")
	flag.IntVar(&compressionLevel, "gzip", gzip.NoCompression, "gzip compression level (0=no compression, 1=best speed, 9=best compression, -1=default)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-b blocksize] [-type mp3|wav|dumb|auto] [-mode streaming|complete] [-gzip 0-9] <file>\n", os.Args[0])
		os.Exit(1)
	}

	filename := flag.Arg(0)

	file, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Auto-detect file type if not specified
	if fileType == "auto" {
		fileType = detectFileType(filename)
	}

	var chunker Chunker
	switch strings.ToLower(fileType) {
	case "mp3":
		if mode != "streaming" {
			fmt.Fprintf(os.Stderr, "Mode %s not supported for MP3 files, only streaming mode is available\n", mode)
			os.Exit(1)
		}
		if compressionLevel != gzip.NoCompression {
			fmt.Fprintf(os.Stderr, "Compression not supported for MP3 files\n")
			os.Exit(1)
		}
		chunker = NewMP3Chunker(file, blockSize, 2048)
	case "wav":
		var wavMode WAVChunkMode
		switch strings.ToLower(mode) {
		case "streaming":
			wavMode = WAVModeStreaming
		case "complete":
			wavMode = WAVModeComplete
		default:
			fmt.Fprintf(os.Stderr, "Unsupported mode: %s. Use 'streaming' or 'complete'\n", mode)
			os.Exit(1)
		}
		chunker = NewWAVChunker(file, blockSize, wavMode, compressionLevel)
	case "dumb":
		if mode != "streaming" {
			fmt.Fprintf(os.Stderr, "Mode %s not supported for dumb files, only streaming mode is available\n", mode)
			os.Exit(1)
		}
		if compressionLevel != gzip.NoCompression {
			fmt.Fprintf(os.Stderr, "Compression not supported for dumb files\n")
			os.Exit(1)
		}
		chunker = NewDumbChunker(file, blockSize)
	default:
		fmt.Fprintf(os.Stderr, "Unsupported file type: %s\n", fileType)
		os.Exit(1)
	}

	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error chunking file: %v\n", err)
			os.Exit(1)
		}

		dataChunk := DataChunk{
			Data: base64.StdEncoding.EncodeToString(chunk),
		}
		if err := json.NewEncoder(os.Stdout).Encode(dataChunk); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
			os.Exit(1)
		}
	}
}

func detectFileType(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".mp3") {
		return "mp3"
	} else if strings.HasSuffix(strings.ToLower(filename), ".wav") {
		return "wav"
	}
	return "mp3" // default to mp3 for unknown extensions
}
