use anyhow::Result;
use base64::{engine::general_purpose, Engine as _};
use clap::{Arg, Command};
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
    tracing_subscriber::fmt::init();
    
    let matches = Command::new("jsonl_player")
        .version("1.0")
        .author("Your Name")
        .about("Plays audio chunks from JSONL stream")
        .arg(
            Arg::new("playback")
                .long("playback")
                .value_name("FORMAT")
                .help("Audio format for playback")
                .value_parser(["mp3", "wav"])
                .default_value("mp3")
        )
        .get_matches();

    let playback_format = matches.get_one::<String>("playback").unwrap();
    
    let (_stream, stream_handle) = OutputStream::try_default()?;
    let sink = Sink::try_new(&stream_handle)?;

    let (tx, rx) = mpsc::channel::<Vec<u8>>();

    let format = playback_format.clone();
    let consumer_thread = thread::spawn(move || {
        for decoded_data in rx {
            let audio_file = Cursor::new(decoded_data);
            let source = match format.as_str() {
                "mp3" => Decoder::new_mp3(audio_file),
                "wav" => Decoder::new_wav(audio_file),
                _ => unreachable!("Invalid format should be caught by clap"),
            };
            
            if let Ok(source) = source {
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