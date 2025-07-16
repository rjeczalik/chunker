package main

import (
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

	flag.IntVar(&blockSize, "b", 8192, "block size for chunking")
	flag.StringVar(&fileType, "type", "auto", "file type: mp3, wav, dumb, or auto")

	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-b blocksize] [-type mp3|wav|dumb|auto] <file>\n", os.Args[0])
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
		chunker = NewMP3Chunker(file, blockSize, 2048)
	case "wav":
		chunker = NewWAVChunker(file, blockSize)
	case "dumb":
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
