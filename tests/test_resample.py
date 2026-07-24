import numpy as np
import soxr

from livecap.audio import BYTES_PER_SAMPLE, CHUNK_BYTES, TARGET_RATE, float_to_pcm16


def _stream(native_rate: int) -> soxr.ResampleStream:
    return soxr.ResampleStream(native_rate, TARGET_RATE, 1, dtype="float32", quality="HQ")


def test_chunk_constant_matches_100ms_at_16k():
    assert CHUNK_BYTES == 3200
    assert CHUNK_BYTES == TARGET_RATE * BYTES_PER_SAMPLE * 100 // 1000


def test_pcm16_encoding_is_little_endian_int16():
    pcm = float_to_pcm16(np.array([0.0, 1.0, -1.0], dtype=np.float32))
    assert len(pcm) == 6
    assert np.frombuffer(pcm, dtype="<i2").tolist() == [0, 32767, -32767]


def test_pcm16_clips_out_of_range_input():
    pcm = np.frombuffer(float_to_pcm16(np.array([2.0, -2.0], dtype=np.float32)), dtype="<i2")
    assert pcm.tolist() == [32767, -32768]


def test_60s_of_48k_yields_expected_16k_byte_count():
    """Acceptance: 60 s of capture lands within 1% of 16000 * 2 * 60 bytes."""
    native = 48_000
    rs = _stream(native)
    block = int(native * 0.02)  # 20ms callback blocks
    total_bytes = 0
    for _ in range(60 * 50):  # 60s worth of 20ms blocks
        out = rs.resample_chunk(np.zeros(block, dtype=np.float32))
        total_bytes += len(float_to_pcm16(out))
    expected = TARGET_RATE * BYTES_PER_SAMPLE * 60
    assert abs(total_bytes - expected) / expected < 0.01


def test_44100_source_also_lands_within_1_percent():
    native = 44_100
    rs = _stream(native)
    block = int(native * 0.02)
    total = 0
    for _ in range(60 * 50):
        total += rs.resample_chunk(np.zeros(block, dtype=np.float32)).size
    expected = TARGET_RATE * 60
    assert abs(total - expected) / expected < 0.01


def test_streaming_resample_has_no_boundary_discontinuity():
    """No click at the chunk seams — this is what the one-shot resample gets wrong."""
    native = 48_000
    freq = 440.0
    block = int(native * 0.02)
    n_blocks = 50
    t = np.arange(block * n_blocks, dtype=np.float32) / native
    sine = np.sin(2 * np.pi * freq * t).astype(np.float32)

    rs = _stream(native)
    pieces = [rs.resample_chunk(sine[i * block : (i + 1) * block]) for i in range(n_blocks)]
    out = np.concatenate(pieces)

    # Skip the filter priming region and look only at steady state.
    steady = out[200:-200]
    # At 16k a 440 Hz sine changes by at most 2*pi*440/16000 ~= 0.173 per sample.
    # A discontinuity at a seam would blow well past that.
    assert np.max(np.abs(np.diff(steady))) < 0.25
    # Amplitude must hold — neither decaying nor diverging. Filter ringing can
    # nudge slightly past 1.0, which is harmless since PCM conversion clips.
    assert 0.9 < np.max(np.abs(steady)) < 1.01
