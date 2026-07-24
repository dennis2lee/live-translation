import pytest

from livecap.audio import RingBuffer


def test_roundtrip_preserves_bytes():
    ring = RingBuffer(1000)
    ring.write(b"abcdefghij")
    assert ring.read(4) == b"abcd"
    assert ring.read(6) == b"efghij"


def test_read_returns_none_when_short():
    ring = RingBuffer(1000)
    ring.write(b"abc")
    assert ring.read(4) is None
    ring.write(b"d")
    assert ring.read(4) == b"abcd"


def test_wraparound_is_contiguous():
    ring = RingBuffer(10)
    ring.write(b"0123456789")
    assert ring.read(6) == b"012345"
    ring.write(b"ABCDEF")  # wraps past the end of the backing array
    assert ring.read(10) == b"6789ABCDEF"


def test_overflow_drops_oldest_and_counts():
    ring = RingBuffer(10)
    ring.write(b"0123456789")
    ring.write(b"ABCDE")  # producer laps the consumer by 5 bytes
    assert ring.read(10) == b"56789ABCDE"
    assert ring.dropped_bytes == 5


def test_write_larger_than_capacity_keeps_last_capacity_bytes():
    ring = RingBuffer(8)
    ring.write(b"0123456789ABCDEF")
    assert ring.read(8) == b"89ABCDEF"


def test_clear_discards_pending():
    ring = RingBuffer(100)
    ring.write(b"abcdef")
    ring.clear()
    assert ring.read(1) is None
    ring.write(b"xy")
    assert ring.read(2) == b"xy"


def test_available_is_capped_at_capacity():
    ring = RingBuffer(10)
    ring.write(b"0123456789ABCDE")
    assert ring.available() == 10


def test_rejects_bad_sizes():
    ring = RingBuffer(10)
    with pytest.raises(ValueError):
        ring.read(0)
    with pytest.raises(ValueError):
        ring.read(11)
    with pytest.raises(ValueError):
        RingBuffer(0)


def test_empty_write_is_noop():
    ring = RingBuffer(10)
    ring.write(b"")
    assert ring.available() == 0


def test_sustained_producer_consumer_cycle():
    """3200바이트(100ms) 단위 소비를 오래 돌려도 어긋나지 않아야 한다."""
    ring = RingBuffer(16000 * 2 * 30)
    payload = bytes(range(256)) * 5  # 1280 bytes
    consumed = bytearray()
    produced = bytearray()
    for _ in range(500):
        ring.write(payload)
        produced += payload
        while (chunk := ring.read(3200)) is not None:
            consumed += chunk
    assert ring.dropped_bytes == 0
    assert bytes(consumed) == bytes(produced[: len(consumed)])
    assert len(produced) - len(consumed) < 3200
