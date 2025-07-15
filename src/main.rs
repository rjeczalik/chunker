use anyhow::Result;
use base64::{engine::general_purpose, Engine as _};
use rodio::{Decoder, OutputStream, Sink};
use serde::Deserialize;
use std::io::{self, BufRead, Cursor};

#[derive(Deserialize)]
struct JsonData {
    data: String,
}

fn main() -> Result<()> {
    let (_stream, stream_handle) = OutputStream::try_default()?;
    let sink = Sink::try_new(&stream_handle)?;

    for line in io::stdin().lock().lines() {
        let line = line?;
        if line.trim().is_empty() {
            continue;
        }

        let json_data: JsonData = serde_json::from_str(&line)?;
        let decoded_data = general_purpose::STANDARD.decode(&json_data.data)?;
        let audio_file = Cursor::new(decoded_data);
        let source = Decoder::new_mp3(audio_file)?;

        sink.append(source);
        sink.sleep_until_end();
    }

    Ok(())
}