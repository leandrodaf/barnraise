import { getToken } from '$lib/api';

function headers() { return { Authorization: 'Bearer ' + getToken() }; }

export async function localDictationAvailable(signal: AbortSignal) {
 if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') return false;
 const r = await fetch('/v1/dictation', { headers: headers(), signal });
 return r.ok && (await r.json()).available === true;
}

export async function recordDictation(signal: AbortSignal, onRecording: (stop: () => void) => void, onTranscribing: () => void): Promise<string> {
 const mimeType = ['audio/webm;codecs=opus','audio/mp4','audio/ogg;codecs=opus'].find(type => MediaRecorder.isTypeSupported(type));
 if (!mimeType) throw new Error('format');
 const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
 try {
  signal.throwIfAborted();
  const rec = new MediaRecorder(stream, { mimeType, audioBitsPerSecond: 48000 });
  const chunks: Blob[] = [];
  const stop = () => { if (rec.state !== 'inactive') rec.stop(); };
  const timer = setTimeout(stop, 120000);
  signal.addEventListener('abort', stop, { once: true });
  try {
   await new Promise<void>((resolve, reject) => {
    rec.ondataavailable = e => { if (e.data.size) chunks.push(e.data); };
    rec.onstop = () => resolve();
    rec.onerror = () => reject(new Error('record'));
    rec.start();
    onRecording(stop);
   });
  } finally {
   clearTimeout(timer);
   signal.removeEventListener('abort', stop);
   stream.getTracks().forEach(track => track.stop());
  }
  signal.throwIfAborted();
  onTranscribing();
  const r = await fetch('/v1/dictation', { method: 'POST', headers: headers(), signal, body: new Blob(chunks, {type: mimeType}) });
  if (!r.ok) throw new Error('transcribe');
  return (await r.json()).text.trim();
 } finally { stream.getTracks().forEach(track => track.stop()); }
}
