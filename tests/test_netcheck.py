"""The speed test, run entirely offline.

Nothing here touches the network: the socket and urlopen calls are replaced, so
the arithmetic and the verdicts are what gets exercised.
"""

from __future__ import annotations

import contextlib
import io
import urllib.error

import pytest

from livecap import netcheck

# --- latency --------------------------------------------------------------


@pytest.fixture
def fake_socket(monkeypatch):
    """Make each handshake appear to take a fixed list of milliseconds."""

    def _install(*durations_ms: float, fail_after: int | None = None):
        clock = {"t": 1000.0}
        calls = {"n": 0}
        queue = list(durations_ms)

        monkeypatch.setattr(netcheck.time, "perf_counter", lambda: clock["t"])

        @contextlib.contextmanager
        def _connect(_addr, timeout=None):
            calls["n"] += 1
            if fail_after is not None and calls["n"] > fail_after:
                raise OSError("connection refused")
            clock["t"] += queue.pop(0) / 1000.0 if queue else 0.0
            yield object()

        monkeypatch.setattr(netcheck.socket, "create_connection", _connect)
        return calls

    return _install


def test_latency_reports_median_best_and_jitter(fake_socket):
    fake_socket(10.0, 20.0, 60.0)
    result = netcheck.measure_latency(samples=3)
    assert result.ok is True
    assert result.best_ms == pytest.approx(10.0)
    assert result.median_ms == pytest.approx(20.0)
    assert result.jitter_ms == pytest.approx(50.0)
    assert result.failures == 0


def test_a_single_sample_has_no_jitter(fake_socket):
    fake_socket(25.0)
    assert netcheck.measure_latency(samples=1).jitter_ms == 0.0


def test_failed_handshakes_are_counted_not_raised(fake_socket):
    fake_socket(15.0, fail_after=1)
    result = netcheck.measure_latency(samples=3)
    assert result.ok is True  # one sample still got through
    assert result.failures == 2
    assert "refused" in result.error


def test_an_unreachable_host_is_reported_not_raised(fake_socket):
    fake_socket(fail_after=0)
    result = netcheck.measure_latency(samples=3)
    assert result.ok is False
    assert result.failures == 3
    assert result.median_ms == 0.0
    assert "unreachable" in netcheck.Report(latency=result).summary_lines()[0]


def test_latency_targets_the_live_api_host_by_default():
    """Latency to anywhere else says nothing about how a session will behave."""
    assert netcheck.LIVE_API_HOST == "generativelanguage.googleapis.com"
    assert netcheck.Latency().host == netcheck.LIVE_API_HOST


# --- throughput -----------------------------------------------------------


@pytest.fixture
def fake_http(monkeypatch):
    """Make a transfer of n bytes appear to take a fixed number of seconds."""

    def _install(payload: bytes = b"", seconds: float = 1.0, error: Exception | None = None):
        clock = {"t": 500.0}
        seen: dict[str, object] = {}

        monkeypatch.setattr(netcheck.time, "perf_counter", lambda: clock["t"])

        @contextlib.contextmanager
        def _urlopen(request, timeout=None):
            seen["url"] = request.full_url
            seen["method"] = request.get_method()
            seen["headers"] = dict(request.header_items())
            seen["body"] = request.data
            if error is not None:
                raise error
            clock["t"] += seconds
            yield io.BytesIO(payload)

        monkeypatch.setattr(netcheck.urllib.request, "urlopen", _urlopen)
        return seen

    return _install


def test_download_speed_is_bytes_over_seconds(fake_http):
    fake_http(payload=b"\0" * 1_000_000, seconds=2.0)
    flow = netcheck.measure_download(nbytes=1_000_000)
    assert flow.ok is True
    assert flow.kbps == pytest.approx(4000.0)  # 1 MB in 2 s = 4 Mbit/s
    assert flow.mbps == pytest.approx(4.0)


def test_download_counts_what_arrived_not_what_was_asked_for(fake_http):
    """A truncated response must not be reported as full speed."""
    fake_http(payload=b"\0" * 250_000, seconds=1.0)
    flow = netcheck.measure_download(nbytes=1_000_000)
    assert flow.bytes_moved == 250_000
    assert flow.kbps == pytest.approx(2000.0)


def test_upload_posts_the_payload(fake_http):
    seen = fake_http(seconds=1.0)
    flow = netcheck.measure_upload(nbytes=500_000)
    assert seen["method"] == "POST"
    assert len(seen["body"]) == 500_000
    assert set(seen["body"]) == {0}, "the payload must be zeros, never real data"
    assert flow.kbps == pytest.approx(4000.0)


def test_the_speed_host_gets_a_user_agent(fake_http):
    """The default Python-urllib agent is answered with 403."""
    seen = fake_http(payload=b"\0", seconds=1.0)
    netcheck.measure_download(nbytes=1)
    agents = [v for k, v in seen["headers"].items() if k.lower() == "user-agent"]
    assert agents == [netcheck.USER_AGENT]


def test_a_transfer_error_is_a_message_not_an_exception(fake_http):
    fake_http(error=urllib.error.URLError("no route to host"))
    flow = netcheck.measure_download()
    assert flow.ok is False
    assert flow.kbps == 0.0
    assert "no route to host" in flow.error


def test_a_zero_length_transfer_is_not_ok(fake_http):
    fake_http(payload=b"", seconds=1.0)
    assert netcheck.measure_download(nbytes=10).ok is False


# --- verdicts -------------------------------------------------------------


def _report(latency_ms: list[float], up_kbps: float, down_kbps: float = 50_000.0):
    """Build a report with the given numbers, without measuring anything."""
    report = netcheck.Report()
    report.latency = netcheck.Latency(samples_ms=list(latency_ms))
    for flow, kbps in ((report.upload, up_kbps), (report.download, down_kbps)):
        flow.seconds = 1.0
        flow.bytes_moved = int(kbps * 1000.0 / 8.0)
    return report


def test_a_healthy_connection_is_called_good():
    assert _report([20.0, 25.0], up_kbps=20_000.0).verdict().startswith("Good")


def test_upload_below_the_audio_rate_is_called_too_slow():
    """A session sends 256 kbit/s upstream; less than that cannot work at all."""
    verdict = _report([20.0], up_kbps=200.0).verdict()
    assert verdict.startswith("Too slow")
    assert "256" in verdict


def test_upload_just_above_the_audio_rate_is_called_tight():
    verdict = _report([20.0], up_kbps=400.0).verdict()
    assert verdict.startswith("Enough for one session")


def test_high_latency_is_reported_even_when_bandwidth_is_fine():
    verdict = _report([450.0, 460.0], up_kbps=50_000.0).verdict()
    assert "behind the speaker" in verdict


def test_jitter_is_reported_even_when_the_median_is_fine():
    """A steady 200 ms is easier to watch than 80 ms that spikes to 400."""
    verdict = _report([20.0, 30.0, 400.0], up_kbps=50_000.0).verdict()
    assert "Unsteady" in verdict


def test_no_connection_at_all_says_so():
    assert netcheck.Report().verdict().startswith("No connection")


def test_a_reachable_host_with_a_failed_speed_test_is_not_called_good():
    """Latency working proves the network is up, but says nothing about speed."""
    report = netcheck.Report()
    report.latency = netcheck.Latency(samples_ms=[20.0])
    report.upload.error = "HTTP Error 403: Forbidden"
    verdict = report.verdict()
    assert not verdict.startswith("Good")
    assert "403" in verdict


def test_an_unreachable_api_host_outranks_the_speed_numbers():
    report = _report([], up_kbps=50_000.0)
    assert "will not connect" in report.verdict()


def test_the_audio_rate_matches_the_wire_format():
    """16 kHz × 16-bit mono is 32 kB/s, which is 256 kbit/s."""
    from livecap.audio import BYTES_PER_SAMPLE, TARGET_RATE

    assert netcheck.AUDIO_KBPS == TARGET_RATE * BYTES_PER_SAMPLE * 8 / 1000.0


# --- the whole run --------------------------------------------------------


def test_run_check_gathers_all_three_measurements(monkeypatch):
    monkeypatch.setattr(
        netcheck, "measure_latency", lambda *a, **k: netcheck.Latency(samples_ms=[15.0])
    )
    monkeypatch.setattr(
        netcheck,
        "measure_upload",
        lambda *a, **k: netcheck.Throughput("upload", 1_000_000, 1.0),
    )
    monkeypatch.setattr(
        netcheck,
        "measure_download",
        lambda *a, **k: netcheck.Throughput("download", 5_000_000, 1.0),
    )
    report = netcheck.run_check()
    assert report.verdict().startswith("Good")
    lines = report.summary_lines()
    assert len(lines) == 3
    assert "Upload: 8.0 Mbit/s" in lines[1]
    assert "Download: 40.0 Mbit/s" in lines[2]
