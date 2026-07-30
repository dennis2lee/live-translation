"""Internet speed test, sized against what a Live session actually needs.

A translation session is a continuous upstream of 16 kHz / 16-bit mono PCM —
32 kB/s, or 256 kbit/s — with transcriptions coming back a fragment at a time.
Upstream capacity and round-trip latency therefore matter far more than download
speed, which is the opposite of what a general speed test emphasises.

Three things are measured:

  * latency and jitter to the Live API host itself, by timing TCP handshakes.
    Nothing is sent, so this costs nothing and needs no API key.
  * upload throughput, which is what carries the audio.
  * download throughput, for completeness.

Throughput uses Cloudflare's public speed-test endpoints. The payload is
zero-filled, so no audio, transcript or key material leaves the machine.

No Qt and no google-genai here, so it stays unit-testable.
"""

from __future__ import annotations

import socket
import statistics
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field

from .util import logging as log

_log = log.get(__name__)

# The host a Live session connects to. Latency to anywhere else is a guess.
LIVE_API_HOST = "generativelanguage.googleapis.com"
LIVE_API_PORT = 443

SPEED_HOST = "speed.cloudflare.com"
DOWNLOAD_URL = f"https://{SPEED_HOST}/__down?bytes=%d"
UPLOAD_URL = f"https://{SPEED_HOST}/__up"

# The default `Python-urllib/3.x` is rejected with 403 by the speed-test host,
# so identify the application instead.
USER_AGENT = "LiveCap/network-check"

LATENCY_SAMPLES = 5
DOWNLOAD_BYTES = 4_000_000
UPLOAD_BYTES = 2_000_000
TIMEOUT_SECONDS = 10.0
# A TCP handshake needs nothing like this long; the cap only bounds how long the
# whole check can take when the network is dead.
LATENCY_TIMEOUT_SECONDS = 3.0

# 16000 samples/s × 2 bytes × 8 bits = 256 kbit/s of audio, one way.
AUDIO_KBPS = 256.0

# Headroom over the bare audio rate. Below this, a session works until anything
# else on the network wants bandwidth.
UPLOAD_COMFORTABLE_KBPS = AUDIO_KBPS * 4

# Above this the captions trail the speaker enough to notice.
LATENCY_OK_MS = 150.0
LATENCY_POOR_MS = 300.0

# Jitter is what actually produces stalls: a steady 200 ms is easier to watch
# than an average of 80 ms that spikes to 400.
JITTER_OK_MS = 40.0
JITTER_POOR_MS = 100.0


@dataclass
class Latency:
    """Round-trip times to a host, from TCP handshakes."""

    host: str = LIVE_API_HOST
    samples_ms: list[float] = field(default_factory=list)
    failures: int = 0
    error: str = ""

    @property
    def ok(self) -> bool:
        return bool(self.samples_ms)

    @property
    def best_ms(self) -> float:
        return min(self.samples_ms) if self.samples_ms else 0.0

    @property
    def median_ms(self) -> float:
        return statistics.median(self.samples_ms) if self.samples_ms else 0.0

    @property
    def jitter_ms(self) -> float:
        """Spread of the samples — the part that causes stalls."""
        if len(self.samples_ms) < 2:
            return 0.0
        return max(self.samples_ms) - min(self.samples_ms)


@dataclass
class Throughput:
    """One direction of a transfer."""

    direction: str = "download"
    bytes_moved: int = 0
    seconds: float = 0.0
    error: str = ""

    @property
    def ok(self) -> bool:
        return self.bytes_moved > 0 and self.seconds > 0.0

    @property
    def kbps(self) -> float:
        """Kilobits per second."""
        if not self.ok:
            return 0.0
        return self.bytes_moved * 8.0 / self.seconds / 1000.0

    @property
    def mbps(self) -> float:
        return self.kbps / 1000.0


@dataclass
class Report:
    latency: Latency = field(default_factory=Latency)
    upload: Throughput = field(default_factory=lambda: Throughput(direction="upload"))
    download: Throughput = field(default_factory=lambda: Throughput(direction="download"))

    def verdict(self) -> str:
        """One line saying whether this connection can carry a session."""
        if not self.latency.ok and not self.upload.ok:
            return "No connection — the network is unreachable"
        if not self.latency.ok:
            return f"Cannot reach {self.latency.host} — a session will not connect"
        if not self.upload.ok:
            # Latency worked, so the network is up; only the speed test failed.
            return (
                f"Latency is {self.latency.median_ms:.0f} ms, but the speed test "
                f"could not run — {self.upload.error or 'no data'}"
            )
        if self.upload.kbps < AUDIO_KBPS:
            return (
                f"Too slow — {self.upload.kbps:.0f} kbit/s up cannot carry the "
                f"{AUDIO_KBPS:.0f} kbit/s of audio a session needs"
            )
        if self.latency.median_ms > LATENCY_POOR_MS:
            return (
                f"Usable, but {self.latency.median_ms:.0f} ms to the API will "
                "leave the captions noticeably behind the speaker"
            )
        if self.latency.jitter_ms > JITTER_POOR_MS:
            return (
                f"Unsteady — latency swings by {self.latency.jitter_ms:.0f} ms, "
                "so expect captions to stall and then catch up"
            )
        if self.upload.kbps < UPLOAD_COMFORTABLE_KBPS:
            return (
                f"Enough for one session ({self.upload.kbps:.0f} kbit/s up), but "
                "with little to spare for anything else on this network"
            )
        return "Good — plenty of headroom for a translation session"

    def summary_lines(self) -> list[str]:
        """Human-readable detail, one measurement per line."""
        out: list[str] = []
        lat = self.latency
        if lat.ok:
            out.append(
                f"Latency to {lat.host}: {lat.median_ms:.0f} ms median, "
                f"{lat.best_ms:.0f} ms best, {lat.jitter_ms:.0f} ms jitter"
                + (f" ({lat.failures} failed)" if lat.failures else "")
            )
        else:
            out.append(f"Latency to {lat.host}: unreachable — {lat.error or 'no response'}")
        for flow in (self.upload, self.download):
            if flow.ok:
                out.append(
                    f"{flow.direction.capitalize()}: {flow.mbps:.1f} Mbit/s "
                    f"({flow.bytes_moved / 1_000_000:.1f} MB in {flow.seconds:.1f} s)"
                )
            else:
                out.append(f"{flow.direction.capitalize()}: failed — {flow.error or 'no data'}")
        return out


# --- measurement ----------------------------------------------------------


def measure_latency(
    host: str = LIVE_API_HOST,
    port: int = LIVE_API_PORT,
    samples: int = LATENCY_SAMPLES,
    timeout: float = LATENCY_TIMEOUT_SECONDS,
) -> Latency:
    """Time TCP handshakes to `host`. One handshake is one round trip.

    ICMP ping is not usable here — it needs a raw socket, and Google's front end
    does not answer it consistently anyway.
    """
    result = Latency(host=host)
    for _ in range(max(1, samples)):
        started = time.perf_counter()
        try:
            with socket.create_connection((host, port), timeout=timeout):
                elapsed = time.perf_counter() - started
        except OSError as exc:
            result.failures += 1
            result.error = str(exc)
            continue
        result.samples_ms.append(elapsed * 1000.0)
    return result


def measure_download(
    nbytes: int = DOWNLOAD_BYTES, timeout: float = TIMEOUT_SECONDS
) -> Throughput:
    """Fetch `nbytes` of zeros and time it."""
    flow = Throughput(direction="download")
    request = urllib.request.Request(
        DOWNLOAD_URL % nbytes, method="GET", headers={"User-Agent": USER_AGENT}
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            read = 0
            while True:
                block = response.read(64 * 1024)
                if not block:
                    break
                read += len(block)
    except (urllib.error.URLError, OSError, ValueError) as exc:
        flow.error = f"{type(exc).__name__}: {exc}"[:120]
        return flow
    flow.seconds = time.perf_counter() - started
    flow.bytes_moved = read
    return flow


def measure_upload(nbytes: int = UPLOAD_BYTES, timeout: float = TIMEOUT_SECONDS) -> Throughput:
    """Post `nbytes` of zeros and time it. This is the direction audio travels."""
    flow = Throughput(direction="upload")
    payload = b"\0" * nbytes
    request = urllib.request.Request(
        UPLOAD_URL,
        data=payload,
        method="POST",
        headers={
            "Content-Type": "application/octet-stream",
            "User-Agent": USER_AGENT,
        },
    )
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            response.read()
    except (urllib.error.URLError, OSError, ValueError) as exc:
        flow.error = f"{type(exc).__name__}: {exc}"[:120]
        return flow
    flow.seconds = time.perf_counter() - started
    flow.bytes_moved = nbytes
    return flow


def run_check() -> Report:
    """Run the whole check. Blocking — call it off the UI thread."""
    report = Report(latency=measure_latency())
    report.upload = measure_upload()
    report.download = measure_download()
    _log.info("network check: %s", report.verdict())
    for line in report.summary_lines():
        _log.info("  %s", line)
    return report
