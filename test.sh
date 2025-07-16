#!/bin/bash

set -euo pipefail

go build
cargo build

export RUST_LOG=debug

./chunker -b=8192 sample.mp3 | ./target/debug/jsonl_player
