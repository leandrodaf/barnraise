import io
import sys
from pathlib import Path

import av
import numpy as np
from faster_whisper import WhisperModel

# Decode only the uploaded recording, with a bound on uncompressed samples.
samples = []
count = 0
resample = av.AudioResampler(format="s16", layout="mono", rate=16000)
with av.open(io.BytesIO(sys.stdin.buffer.read()), format=sys.argv[1],
             options={"protocol_whitelist": "pipe"}) as recording:
    for frame in recording.decode(audio=0):
        for chunk in resample.resample(frame):
            count += chunk.samples
            if count > 125 * 16000:
                raise ValueError("Recording exceeds two minutes")
            samples.append(chunk.to_ndarray().reshape(-1))
    samples.extend(chunk.to_ndarray().reshape(-1) for chunk in resample.resample(None))
if samples:
    audio = np.concatenate(samples).astype(np.float32) / 32768
    model = WhisperModel("base", device="cpu", compute_type="int8", cpu_threads=4,
                         local_files_only=True,
                         download_root=str(Path.home()/".local/share/barnraise-dictation/models"))
    segments, _ = model.transcribe(audio, vad_filter=True, beam_size=3)
    print(" ".join(segment.text.strip() for segment in segments))
