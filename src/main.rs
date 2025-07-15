use anyhow::Result;
use base64::{engine::general_purpose, Engine as _};
use rodio::{Decoder, OutputStream, Sink};
use serde::Deserialize;
use std::io::{self, BufRead, Cursor};
use std::sync::mpsc;
use std::thread;

#[derive(Deserialize)]
struct JsonData {
    data: String,
}

fn main() -> Result<()> {
    let (_stream, stream_handle) = OutputStream::try_default()?;
    let sink = Sink::try_new(&stream_handle)?;

    let (tx, rx) = mpsc::channel::<Vec<u8>>();

    let consumer_thread = thread::spawn(move || {
        for decoded_data in rx {
            let audio_file = Cursor::new(decoded_data);
            if let Ok(source) = Decoder::new_mp3(audio_file) {
                sink.append(source);
            }
        }
        // Wait for the last sound to finish playing.
        sink.sleep_until_end();
    });

    let stdin = io::stdin();
    let handle = stdin.lock();

    for line in handle.lines() {
        let line = match line {
            Ok(line) => line,
            Err(_) => break,
        };

        if line.trim().is_empty() {
            continue;
        }

        if let Ok(json_data) = serde_json::from_str::<JsonData>(&line) {
            if let Ok(decoded_data) = general_purpose::STANDARD.decode(&json_data.data) {
                if tx.send(decoded_data).is_err() {
                    break;
                }
            }
        }
    }

    drop(tx);

    consumer_thread.join().expect("Consumer thread panicked");

    Ok(())
}