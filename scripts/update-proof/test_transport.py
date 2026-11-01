#!/usr/bin/env python3
"""Unit checks for relay plumbing, never acceptance of a shipped update.

Requests traverse a real HTTP CONNECT tunnel and a verified TLS connection to
Relay. Only the relay's upstream opener is a fixture. These arbitrary bytes are
not authenticated manifests, published artifacts, or a substitute updater.
Run through nocx-sky-test, not on the developer host.
"""

import base64
from concurrent.futures import ThreadPoolExecutor
import http.client
import importlib.util
import io
from pathlib import Path
import ssl
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error


DIRECTORY = Path(__file__).resolve().parent
COMMON_SPEC = importlib.util.spec_from_file_location(
    "update_proof_transport_common", DIRECTORY / "common.py")
common = importlib.util.module_from_spec(COMMON_SPEC)
COMMON_SPEC.loader.exec_module(common)
SPEC = importlib.util.spec_from_file_location(
    "update_proof_transport", DIRECTORY / "transport.py")
transport = importlib.util.module_from_spec(SPEC)
with mock.patch.dict(sys.modules, {"common": common}):
    SPEC.loader.exec_module(transport)


class UpstreamResponse(io.BytesIO):
    def __init__(self, payload, status=200):
        super().__init__(payload)
        self.status = status
        self.headers = {"Content-Length": str(len(payload))}


class FixtureOpener:
    """The only replaced network seam; no external host is contacted."""

    def __init__(self):
        self.responses = {}
        self.requests = []

    def open(self, request, timeout):
        url = request if isinstance(request, str) else request.full_url
        self.requests.append((url, timeout))
        value = self.responses[url]
        if isinstance(value, Exception):
            raise value
        return UpstreamResponse(value)


class TransportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        temporary = tempfile.TemporaryDirectory(prefix="nocx-relay-unit-")
        cls.addClassCleanup(temporary.cleanup)
        cls.directory = Path(temporary.name)
        cls.pairs = {}
        # Deliberately non-JSON bodies, binary data, and distinct detached
        # signatures expose accidental parsing, newline or encoding changes.
        cls.payloads = {
            "stale": (b'previous \x00\xff manifest\r\n', b'previous signature\r\n'),
            "candidate": (b'candidate \xff\x00 manifest\n', b'candidate signature\n'),
            "lower": (b'lower \x00 manifest\r\n', b'lower signature\x00\xff'),
        }
        for label, payloads in cls.payloads.items():
            pair = (cls.directory / (label + ".json"),
                    cls.directory / (label + ".json.sig"))
            for path, payload in zip(pair, payloads):
                path.write_bytes(payload)
            cls.pairs[label] = pair
        cls.relay = transport.Relay(cls.directory, cls.pairs["stale"],
                                    cls.pairs["candidate"], cls.pairs["lower"])
        cls.addClassCleanup(cls.relay.close)
        cls.context = ssl.create_default_context(cafile=str(cls.relay.cert))

    def setUp(self):
        self.relay.mode = "live"
        self.relay.requests.clear()
        self.opener = FixtureOpener()
        self.relay.upstream = self.opener
        for suffix, payload in zip(("manifest.json", "manifest.json.sig"),
                                   self.payloads["candidate"]):
            self.opener.responses[transport.BASE + suffix] = payload

    def request(self, path, *, context=None, relay=None):
        # HTTPSConnection wraps the CONNECT socket with server_hostname set to
        # github.com, so normal CA and hostname verification remain enabled.
        relay = relay or self.relay
        connection = http.client.HTTPSConnection(
            "127.0.0.1", relay.server.server_port,
            context=context or self.context, timeout=5)
        self.addCleanup(connection.close)
        connection.set_tunnel("github.com", 443)
        connection.request("GET", path)
        response = connection.getresponse()
        payload = response.read()
        status = response.status
        # Content-Length can complete before the handler records its evidence.
        # Wait for its declared end (EOF), not a delay or a polling retry.
        if connection.sock is not None:
            self.assertEqual(connection.sock.recv(1), b"")
        connection.close()
        return status, payload

    def fetch_document(self, signature=False):
        suffix = "manifest.json.sig" if signature else "manifest.json"
        return self.request("/shady2k/nocx/releases/download/nightly/" + suffix)

    def test_stale_and_lower_forward_the_exact_saved_document_pairs(self):
        for mode in ("stale", "lower"):
            with self.subTest(mode=mode):
                self.relay.mode = mode
                self.relay.requests.clear()
                for signature in (False, True):
                    self.assertEqual(self.fetch_document(signature),
                                     (200, self.payloads[mode][int(signature)]))
                self.assertEqual([entry["mode"] for entry in self.relay.requests],
                                 [mode, mode])
                self.assertTrue(all(entry.get("ok") for entry in self.relay.requests))
        self.assertEqual(self.opener.requests, [], "refusal pairs must not fetch upstream")

    def test_unsigned_changes_only_detached_signature_not_candidate_manifest(self):
        self.relay.mode = "unsigned"
        self.assertEqual(self.fetch_document(), (200, self.payloads["candidate"][0]))
        status, signature = self.fetch_document(signature=True)
        self.assertEqual(status, 200)
        self.assertEqual(signature, base64.b64encode(bytes(64)) + b"\n")
        self.assertNotEqual(signature, self.payloads["candidate"][1])
        self.assertEqual(self.pairs["candidate"][1].read_bytes(),
                         self.payloads["candidate"][1], "fixture file must remain intact")
        self.assertEqual(self.opener.requests, [])
        self.assertEqual([entry["mode"] for entry in self.relay.requests],
                         ["unsigned", "unsigned"])

    def test_live_fetches_and_returns_exact_candidate_pair_over_tls(self):
        for signature in (False, True):
            self.assertEqual(self.fetch_document(signature),
                             (200, self.payloads["candidate"][int(signature)]))
        self.assertEqual(self.opener.requests, [
            (transport.BASE + "manifest.json", 120),
            (transport.BASE + "manifest.json.sig", 120)])
        self.assertEqual(self.relay.requests, [
            {"mode": "live", "path": "/shady2k/nocx/releases/download/nightly/manifest.json",
             "ok": True},
            {"mode": "live", "path": "/shady2k/nocx/releases/download/nightly/manifest.json.sig",
             "ok": True}])

    def test_live_refuses_mismatched_manifest_and_signature_without_leaking_them(self):
        for suffix in ("manifest.json", "manifest.json.sig"):
            with self.subTest(suffix=suffix):
                unexpected = b"upstream alias moved: unauthenticated changed document"
                self.opener.responses[transport.BASE + suffix] = unexpected
                self.relay.requests.clear()
                status, payload = self.request(
                    "/shady2k/nocx/releases/download/nightly/" + suffix)
                self.assertEqual(status, 502)
                self.assertNotIn(unexpected, payload)
                self.assertEqual(len(self.relay.requests), 1)
                self.assertNotIn("ok", self.relay.requests[0])
                self.assertIn("alias changed", self.relay.requests[0]["error"])

    def test_live_never_falls_back_to_saved_candidate_when_upstream_fails(self):
        for error in (OSError("upstream disconnected"),
                      urllib.error.HTTPError(transport.BASE + "manifest.json", 404,
                                             "not published", {}, None)):
            with self.subTest(error=error):
                self.opener.responses[transport.BASE + "manifest.json"] = error
                self.relay.requests.clear()
                status, payload = self.fetch_document()
                self.assertEqual(status, 502)
                self.assertNotIn(self.payloads["candidate"][0], payload)
                self.assertIn("error", self.relay.requests[0])
                self.assertNotIn("ok", self.relay.requests[0])

    def test_artifact_bytes_always_come_from_upstream_in_every_mode(self):
        payload = bytes(range(256)) * 8193  # Exercise the multi-chunk streaming path.
        for mode in ("live", "stale", "lower", "unsigned"):
            for tag in ("nightly", "nightly-20261026-123"):
                with self.subTest(mode=mode, tag=tag):
                    self.relay.mode = mode
                    path = ("/shady2k/nocx/releases/download/" + tag + "/nocx.AppImage")
                    self.opener.responses["https://github.com" + path] = payload
                    self.assertEqual(self.request(path), (200, payload))
                    self.assertEqual(self.opener.requests[-1],
                                     ("https://github.com" + path, 120))
        self.assertEqual(len(self.opener.requests), 8)
        self.assertEqual(self.relay.requests, [], "artifact must not count as a manifest probe")

    def test_only_the_two_exact_nightly_manifest_paths_are_overridden(self):
        self.relay.mode = "stale"
        paths = [
            "/manifest.json",
            "/another/nocx/releases/download/nightly/manifest.json",
            "/shady2k/nocx/releases/download/nightly-20261026-123/manifest.json",
            "/shady2k/nocx/releases/download/nightly/manifest.json?other=1",
            "/shady2k/nocx/releases/download/nightly/nested/manifest.json",
            "/shady2k/nocx/releases/download/nightly/manifest.json.sig.extra",
        ]
        for path in paths:
            with self.subTest(path=path):
                payload = ("upstream response for " + path).encode()
                self.opener.responses["https://github.com" + path] = payload
                self.assertEqual(self.request(path), (200, payload))
        self.assertEqual(len(self.opener.requests), len(paths))
        self.assertEqual(self.relay.requests, [])

    def test_signature_barrier_holds_response_until_released_and_resets_per_check(self):
        executor = ThreadPoolExecutor(max_workers=1)
        # Always release before waiting for the worker during cleanup, including
        # when an assertion fails. There is no timing-based success condition.
        self.addCleanup(executor.shutdown, wait=True)
        self.addCleanup(self.relay.release_check)
        for mode in ("stale", "live"):
            with self.subTest(mode=mode):
                self.relay.mode = mode
                self.relay.requests.clear()
                self.relay.arm_check()
                self.assertFalse(self.relay.signature_reached.is_set())
                self.assertFalse(self.relay.signature_continue.is_set())
                future = executor.submit(self.fetch_document, True)
                try:
                    self.assertTrue(self.relay.signature_reached.wait(timeout=5),
                                    "TLS signature request must reach the barrier")
                    self.assertFalse(future.done(), "signature response escaped its barrier")
                    self.assertEqual(self.relay.requests, [])
                finally:
                    self.relay.release_check()
                expected = self.payloads["candidate" if mode == "live" else mode][1]
                self.assertEqual(future.result(timeout=5), (200, expected))
                self.assertTrue(self.relay.requests[0].get("ok"))

    def test_live_preflight_fetches_both_documents_without_arming_desktop_barrier(self):
        self.relay.verify_live()
        self.assertEqual(self.opener.requests, [
            (transport.BASE + "manifest.json", 60),
            (transport.BASE + "manifest.json.sig", 60)])
        self.assertEqual(self.relay.requests, [],
                         "producer preflight must not satisfy a desktop request")

    def test_live_preflight_refuses_changed_or_missing_producer_documents(self):
        for suffix in ("manifest.json", "manifest.json.sig"):
            for value in (b"producer moved", OSError("producer missing")):
                with self.subTest(suffix=suffix, value=value):
                    self.opener.responses[transport.BASE + suffix] = value
                    with self.assertRaises(RuntimeError):
                        self.relay.verify_live()
                    self.assertEqual(self.relay.requests, [])
                    # Restore so each subtest reaches the intended document.
                    index = int(suffix.endswith(".sig"))
                    self.opener.responses[transport.BASE + suffix] = self.payloads["candidate"][index]

    def stable_relay(self):
        relay = transport.Relay(self.directory, self.pairs["stale"],
                                self.pairs["candidate"], self.pairs["lower"],
                                channel="stable")
        self.addCleanup(relay.close)
        return relay

    def test_stable_relay_intercepts_only_the_latest_manifest_documents(self):
        relay = self.stable_relay()
        relay.mode = "stale"
        opener = FixtureOpener()
        relay.upstream = opener
        context = ssl.create_default_context(cafile=str(relay.cert))
        for suffix, payload in zip(("manifest.json", "manifest.json.sig"),
                                   self.payloads["stale"]):
            self.assertEqual(
                self.request("/shady2k/nocx/releases/latest/download/" + suffix,
                             context=context, relay=relay),
                (200, payload))
        self.assertEqual([entry["mode"] for entry in relay.requests], ["stale", "stale"])
        self.assertTrue(all(entry.get("ok") for entry in relay.requests))
        self.assertEqual(opener.requests, [], "a refusal pair must not fetch upstream")
        # The versioned location the redirect resolves to is not the channel
        # base: a stable check may request it after a real redirect, and that
        # request must reach production, not a saved probe document.
        versioned = "/shady2k/nocx/releases/download/v0.8.1/manifest.json"
        payload = b"promoted stable manifest from production"
        opener.responses["https://github.com" + versioned] = payload
        self.assertEqual(self.request(versioned, context=context, relay=relay), (200, payload))
        self.assertEqual(opener.requests, [("https://github.com" + versioned, 120)])

    def test_stable_live_preflight_fetches_the_latest_documents(self):
        relay = self.stable_relay()
        opener = FixtureOpener()
        relay.upstream = opener
        for suffix, payload in zip(("manifest.json", "manifest.json.sig"),
                                   self.payloads["candidate"]):
            opener.responses[transport.BASES["stable"] + suffix] = payload
        relay.verify_live()
        self.assertEqual(opener.requests, [
            (transport.BASES["stable"] + "manifest.json", 60),
            (transport.BASES["stable"] + "manifest.json.sig", 60)])
        self.assertEqual(relay.requests, [],
                         "preflight must not satisfy a desktop request")

    def test_stable_live_preflight_refuses_changed_production_documents(self):
        relay = self.stable_relay()
        opener = FixtureOpener()
        relay.upstream = opener
        opener.responses[transport.BASES["stable"] + "manifest.json"] = b"another release"
        opener.responses[transport.BASES["stable"] + "manifest.json.sig"] = \
            self.payloads["candidate"][1]
        with self.assertRaisesRegex(RuntimeError, "stable release does not match"):
            relay.verify_live()

    def test_relay_refuses_an_unknown_channel(self):
        with self.assertRaisesRegex(common.ProofError, "unknown update channel"):
            transport.Relay(self.directory, self.pairs["stale"], self.pairs["candidate"],
                            self.pairs["lower"], channel="beta")

    def test_unknown_mode_fails_closed_instead_of_silently_using_live_bytes(self):
        self.relay.mode = "typo"
        status, _ = self.fetch_document()
        self.assertEqual(status, 502)
        self.assertEqual(self.opener.requests, [])
        self.assertIn("error", self.relay.requests[0])


if __name__ == "__main__":
    unittest.main()
