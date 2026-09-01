import importlib.util
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).with_name("upload_gitcode_assets.py")
SPEC = importlib.util.spec_from_file_location("upload_gitcode_assets", MODULE_PATH)
upload = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(upload)


class FakeTransport:
    def __init__(self, existing=None):
        self.assets = dict(existing or {})
        self.calls = []
        self.pending_name = ""

    def __call__(self, method, url, headers, body):
        self.calls.append((method, url, dict(headers), body))
        if method == "GET" and "/upload_url?" in url:
            self.pending_name = url.split("file_name=", 1)[1]
            return {"data": {"upload_url": "https://storage.example/upload-secret", "headers": {"x-obs-callback": "secret-callback"}}}
        if method == "PUT":
            from urllib.parse import unquote_plus

            self.assets[unquote_plus(self.pending_name)] = len(body)
            return {}
        return {"data": {"assets": {"assets": [{"name": name, "size": size} for name, size in self.assets.items()]}}}


class UploadGitCodeAssetsTest(unittest.TestCase):
    def asset(self, directory, name="artifact.tar.gz", body=b"artifact"):
        path = Path(directory) / name
        path.write_bytes(body)
        return path

    def test_uploads_with_presigned_headers_and_verifies(self):
        with tempfile.TemporaryDirectory() as directory:
            transport = FakeTransport()
            asset = self.asset(directory)
            result = upload.upload_assets(repo="owner/repo", tag="v0.2.1", assets=[asset], token="token", request=transport, verify_delay=0)
        self.assertEqual(result, [{"name": "artifact.tar.gz", "size": 8, "status": "uploaded"}])
        put = next(call for call in transport.calls if call[0] == "PUT")
        self.assertEqual(put[2], {"x-obs-callback": "secret-callback"})
        self.assertNotIn("Authorization", put[2])
        self.assertEqual(put[3], b"artifact")

    def test_exact_existing_asset_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            transport = FakeTransport({"artifact.tar.gz": 8})
            result = upload.upload_assets(repo="owner/repo", tag="v0.2.1", assets=[self.asset(directory)], token="token", request=transport, verify_delay=0)
        self.assertEqual(result[0]["status"], "already_present")
        self.assertFalse(any(call[0] == "PUT" for call in transport.calls))

    def test_existing_size_mismatch_fails_without_upload(self):
        with tempfile.TemporaryDirectory() as directory:
            transport = FakeTransport({"artifact.tar.gz": 7})
            with self.assertRaisesRegex(upload.UploadError, "size differs"):
                upload.upload_assets(repo="owner/repo", tag="v0.2.1", assets=[self.asset(directory)], token="token", request=transport, verify_delay=0)
        self.assertFalse(any(call[0] == "PUT" for call in transport.calls))

    def test_missing_api_size_is_verified_through_download_url(self):
        with tempfile.TemporaryDirectory() as directory:
            asset = self.asset(directory)

            def transport(method, url, headers, body):
                return {"assets": [{"name": "artifact.tar.gz", "browser_download_url": "https://download.example/artifact"}]}

            downloads = []
            result = upload.upload_assets(
                repo="owner/repo",
                tag="v0.2.1",
                assets=[asset],
                token="token",
                request=transport,
                download_size=lambda url: downloads.append(url) or 8,
                verify_delay=0,
            )
        self.assertEqual(result[0]["status"], "already_present")
        self.assertEqual(downloads, ["https://download.example/artifact"])

    def test_missing_api_size_with_wrong_download_size_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            asset = self.asset(directory)

            def transport(method, url, headers, body):
                return {"assets": [{"name": "artifact.tar.gz", "browser_download_url": "https://download.example/artifact"}]}

            with self.assertRaisesRegex(upload.UploadError, "size differs"):
                upload.upload_assets(
                    repo="owner/repo",
                    tag="v0.2.1",
                    assets=[asset],
                    token="token",
                    request=transport,
                    download_size=lambda url: 7,
                    verify_delay=0,
                )

    def test_rejects_non_https_upload_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            asset = self.asset(directory)

            def transport(method, url, headers, body):
                if "/upload_url?" in url:
                    return {"upload_url": "http://storage.example/secret", "headers": {}}
                return {"assets": []}

            with self.assertRaisesRegex(upload.UploadError, "HTTPS"):
                upload.upload_assets(repo="owner/repo", tag="v0.2.1", assets=[asset], token="token", request=transport, verify_delay=0)

    def test_forwards_list_shaped_contract_headers(self):
        with tempfile.TemporaryDirectory() as directory:
            transport = FakeTransport()
            asset = self.asset(directory)

            def request(method, url, headers, body):
                if method == "GET" and "/upload_url?" in url:
                    transport.pending_name = url.split("file_name=", 1)[1]
                    return {
                        "url": "https://storage.example/upload-secret",
                        "headers": [
                            {"key": "Content-Type", "value": "application/octet-stream"},
                            {"key": "x-obs-callback", "value": "secret-callback"},
                        ],
                    }
                return transport(method, url, headers, body)

            upload.upload_assets(repo="owner/repo", tag="v0.2.1", assets=[asset], token="token", request=request, verify_delay=0)
        put = next(call for call in transport.calls if call[0] == "PUT")
        self.assertEqual(put[2]["Content-Type"], "application/octet-stream")
        self.assertEqual(put[2]["x-obs-callback"], "secret-callback")

    def test_partial_readback_fails_without_exposing_contract(self):
        with tempfile.TemporaryDirectory() as directory:
            asset = self.asset(directory)

            def request(method, url, headers, body):
                if method == "GET" and "/upload_url?" in url:
                    return {"url": "https://storage.example/upload-secret", "headers": {"x-obs-callback": "secret-callback"}}
                return {"assets": []}

            with self.assertRaisesRegex(upload.UploadError, "artifact.tar.gz") as failure:
                upload.upload_assets(
                    repo="owner/repo",
                    tag="v0.2.1",
                    assets=[asset],
                    token="token",
                    request=request,
                    verify_attempts=2,
                    verify_delay=0,
                )
        self.assertNotIn("upload-secret", str(failure.exception))
        self.assertNotIn("secret-callback", str(failure.exception))

    def test_put_retries_transient_timeout_with_transfer_budget(self):
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = b"{}"
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=[TimeoutError(), response]) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                result = upload._urllib_request("PUT", "https://storage.example/object", {}, b"payload")
        self.assertEqual(result, {})
        self.assertEqual(urlopen.call_count, 2)
        self.assertEqual(urlopen.call_args.kwargs["timeout"], upload.UPLOAD_REQUEST_TIMEOUT_SECONDS)
        sleep.assert_called_once_with(upload.TRANSPORT_RETRY_DELAY_SECONDS)

    def test_non_replay_safe_request_is_not_retried(self):
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=TimeoutError()) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                with self.assertRaisesRegex(upload.UploadError, "transport_error"):
                    upload._urllib_request("POST", "https://api.example/release", {}, b"payload")
        self.assertEqual(urlopen.call_count, 1)
        sleep.assert_not_called()

    def test_put_retry_exhaustion_is_bounded_and_public_safe(self):
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=TimeoutError()) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                with self.assertRaisesRegex(upload.UploadError, "transport_error") as failure:
                    upload._urllib_request("PUT", "https://storage.example/upload-secret", {"x-secret": "value"}, b"payload")
        self.assertEqual(urlopen.call_count, upload.TRANSPORT_ATTEMPTS)
        self.assertEqual(
            [call.args[0] for call in sleep.call_args_list],
            [upload.TRANSPORT_RETRY_DELAY_SECONDS, upload.TRANSPORT_RETRY_DELAY_SECONDS * 2],
        )
        self.assertNotIn("upload-secret", str(failure.exception))
        self.assertNotIn("x-secret", str(failure.exception))

    def test_non_retryable_http_error_fails_once(self):
        failure = urllib.error.HTTPError("https://storage.example/upload-secret", 403, "forbidden", {}, None)
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=failure) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                with self.assertRaisesRegex(upload.UploadError, "403"):
                    upload._urllib_request("PUT", "https://storage.example/upload-secret", {}, b"payload")
        self.assertEqual(urlopen.call_count, 1)
        sleep.assert_not_called()

    def test_header_validation_failure_is_sanitized_without_retry(self):
        validation = ValueError("Invalid header value b'secret-callback\\nleak'")
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=validation) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                with self.assertRaisesRegex(upload.UploadError, "transport_error") as failure:
                    upload._urllib_request(
                        "PUT",
                        "https://storage.example/upload-secret",
                        {"x-obs-callback": "secret-callback\\nleak"},
                        b"payload",
                    )
        self.assertEqual(urlopen.call_count, 1)
        sleep.assert_not_called()
        self.assertNotIn("secret-callback", str(failure.exception))
        self.assertNotIn("upload-secret", str(failure.exception))

    def test_download_size_uses_content_range_without_full_download(self):
        response = mock.MagicMock()
        response.__enter__.return_value.status = 206
        response.__enter__.return_value.headers = {"Content-Range": "bytes 0-0/6419356"}
        response.__enter__.return_value.read.return_value = b"x"
        with mock.patch.object(upload.urllib.request, "urlopen", return_value=response) as urlopen:
            size = upload._urllib_download_size("https://download.example/artifact")
        self.assertEqual(size, 6419356)
        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header("Range"), "bytes=0-0")
        self.assertEqual(urlopen.call_args.kwargs["timeout"], upload.READBACK_REQUEST_TIMEOUT_SECONDS)

    def test_malformed_partial_range_fails_closed(self):
        response = mock.MagicMock()
        response.__enter__.return_value.status = 206
        response.__enter__.return_value.headers = {}
        response.__enter__.return_value.read.return_value = b"x"
        with mock.patch.object(upload.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(upload.UploadError, "download verification failed"):
                upload._urllib_download_size("https://download.example/artifact")

    def test_range_ignored_200_counts_the_full_body(self):
        response = mock.MagicMock()
        response.__enter__.return_value.status = 200
        response.__enter__.return_value.headers = {}
        response.__enter__.return_value.read.side_effect = [b"full", b"-body", b""]
        with mock.patch.object(upload.urllib.request, "urlopen", return_value=response):
            size = upload._urllib_download_size("https://download.example/artifact")
        self.assertEqual(size, 9)

    def test_download_size_retries_transient_timeout(self):
        response = mock.MagicMock()
        response.__enter__.return_value.status = 206
        response.__enter__.return_value.headers = {"Content-Range": "bytes 0-0/8"}
        response.__enter__.return_value.read.return_value = b"x"
        with mock.patch.object(upload.urllib.request, "urlopen", side_effect=[TimeoutError(), response]) as urlopen:
            with mock.patch.object(upload.time, "sleep") as sleep:
                size = upload._urllib_download_size("https://download.example/artifact")
        self.assertEqual(size, 8)
        self.assertEqual(urlopen.call_count, 2)
        sleep.assert_called_once_with(upload.TRANSPORT_RETRY_DELAY_SECONDS)


if __name__ == "__main__":
    unittest.main()
