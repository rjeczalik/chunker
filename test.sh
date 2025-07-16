#!/bin/bash

set -euo pipefail

sample_type=${1:-mp3}

go build
cargo build

export RUST_LOG=debug

# ./chunker -b=8192 -type mp3 sample.mp3 | ./target/debug/jsonl_player --playback mp3
# ./chunker -b=8192 -type wav sample.wav | ./target/debug/jsonl_player --playback wav

./chunker -b=8192 -type ${sample_type} sample.${sample_type} | ./target/debug/jsonl_player --playback ${sample_type}
