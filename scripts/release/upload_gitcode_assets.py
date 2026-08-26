#!/usr/bin/env python3
"""Upload release artifacts through GitCode's presigned attachment flow."""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Callable, Dict, List, NamedTuple, Optional, Tuple


class UploadError(RuntimeError):
    """A public-safe release upload failure."""


Request = Callable[[str, str, Dict[str, str], Optional[bytes]], Any]
DownloadSize = Callable[[str], int]


class ReleaseAsset(NamedTuple):
    size: Optional[int]
    download_url: str


def _urllib_request(method: str, url: str, headers: Dict[str, str], body: Optional[bytes]) -> Any:
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            payload = response.read()
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError) as exc:
        status = getattr(exc, "code", "transport_error")
        raise UploadError(f"release asset request failed ({status})") from None
    if not payload:
        return {}
    try:
        return json.loads(payload)
    except json.JSONDecodeError:
        return {}


def _unwrap(payload: Any) -> Any:
    if isinstance(payload, dict) and isinstance(payload.get("data"), (dict, list)):
        return payload["data"]
    return payload


def _release_assets(payload: Any) -> Dict[str, ReleaseAsset]:
    release = _unwrap(payload)
    if not isinstance(release, dict):
        raise UploadError("GitCode release response is not an object")
    assets: Any = release.get("assets", [])
    if isinstance(assets, dict):
        assets = assets.get("assets", [])
    if not isinstance(assets, list):
        raise UploadError("GitCode release assets response is not a list")
    result: Dict[str, ReleaseAsset] = {}
    for asset in assets:
        if not isinstance(asset, dict):
            continue
        name = str(asset.get("name") or "").strip()
        if not name:
            continue
        raw_size = asset.get("size")
        try:
            size = int(raw_size) if raw_size not in (None, "") else None
        except (TypeError, ValueError):
            size = None
        download_url = str(asset.get("browser_download_url") or "").strip()
        result[name] = ReleaseAsset(size=size, download_url=download_url)
    return result


def _urllib_download_size(url: str) -> int:
    if urllib.parse.urlparse(url).scheme != "https":
        raise UploadError("GitCode release asset did not return an HTTPS download URL")
    request = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            size = 0
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    return size
                size += len(chunk)
    except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError):
        raise UploadError("GitCode release asset download verification failed") from None


def _verified_size(asset: ReleaseAsset, download_size: DownloadSize, cache: Dict[str, int]) -> Optional[int]:
    if asset.size is not None:
        return asset.size
    if not asset.download_url:
        return None
    if asset.download_url not in cache:
        cache[asset.download_url] = download_size(asset.download_url)
    return cache[asset.download_url]


def _upload_contract(payload: Any) -> Tuple[str, Dict[str, str]]:
    contract = _unwrap(payload)
    if not isinstance(contract, dict):
        raise UploadError("GitCode upload contract is not an object")
    url = str(contract.get("upload_url") or contract.get("url") or "").strip()
    if urllib.parse.urlparse(url).scheme != "https":
        raise UploadError("GitCode upload contract did not return an HTTPS URL")
    raw_headers = contract.get("headers") or contract.get("request_headers") or {}
    headers: Dict[str, str] = {}
    if isinstance(raw_headers, dict):
        headers = {str(key): str(value) for key, value in raw_headers.items()}
    elif isinstance(raw_headers, list):
        for item in raw_headers:
            if isinstance(item, dict) and item.get("key") is not None:
                headers[str(item["key"])] = str(item.get("value") or "")
    else:
        raise UploadError("GitCode upload contract headers are invalid")
    return url, headers


def upload_assets(
    *,
    repo: str,
    tag: str,
    assets: List[Path],
    token: str,
    api_base_url: str = "https://api.gitcode.com/api/v5",
    request: Request = _urllib_request,
    download_size: DownloadSize = _urllib_download_size,
    verify_attempts: int = 5,
    verify_delay: float = 1.0,
) -> List[Dict[str, Any]]:
    owner, separator, name = repo.strip().partition("/")
    if not separator or not owner or not name or "/" in name:
        raise UploadError("repo must use owner/name form")
    if not tag.strip():
        raise UploadError("tag is required")
    if not token.strip():
        raise UploadError("GitCode token is required")
    base = api_base_url.rstrip("/")
    repo_path = "/repos/{}/{}/releases".format(urllib.parse.quote(owner, safe=""), urllib.parse.quote(name, safe=""))
    release_url = base + repo_path + "/tags/" + urllib.parse.quote(tag, safe="")
    api_headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
    existing = _release_assets(request("GET", release_url, api_headers, None))
    downloaded_sizes: Dict[str, int] = {}
    result: List[Dict[str, Any]] = []
    expected: Dict[str, int] = {}
    for path in assets:
        path = path.resolve()
        if not path.is_file():
            raise UploadError(f"release asset is not a file: {path.name}")
        asset_name, size = path.name, path.stat().st_size
        if asset_name in expected:
            raise UploadError(f"duplicate release asset name: {asset_name}")
        expected[asset_name] = size
        if asset_name in existing:
            if _verified_size(existing[asset_name], download_size, downloaded_sizes) != size:
                raise UploadError(f"existing release asset size differs: {asset_name}")
            result.append({"name": asset_name, "size": size, "status": "already_present"})
            continue
        contract_url = base + repo_path + "/" + urllib.parse.quote(tag, safe="") + "/upload_url?" + urllib.parse.urlencode({"file_name": asset_name})
        upload_url, upload_headers = _upload_contract(request("GET", contract_url, api_headers, None))
        request("PUT", upload_url, upload_headers, path.read_bytes())
        result.append({"name": asset_name, "size": size, "status": "uploaded"})

    verified: Dict[str, ReleaseAsset] = {}
    for attempt in range(max(1, verify_attempts)):
        verified = _release_assets(request("GET", release_url, api_headers, None))
        if all(
            asset_name in verified
            and _verified_size(verified[asset_name], download_size, downloaded_sizes) == size
            for asset_name, size in expected.items()
        ):
            return result
        if attempt + 1 < verify_attempts:
            time.sleep(verify_delay)
    missing = sorted(
        asset_name
        for asset_name, size in expected.items()
        if asset_name not in verified
        or _verified_size(verified[asset_name], download_size, downloaded_sizes) != size
    )
    raise UploadError("GitCode release asset verification failed: " + ", ".join(missing))


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--asset", action="append", required=True, type=Path)
    parser.add_argument("--api-base-url", default="https://api.gitcode.com/api/v5")
    parser.add_argument("--token-env", default="GITCODE_TOKEN")
    args = parser.parse_args(argv)
    try:
        result = upload_assets(repo=args.repo, tag=args.tag, assets=args.asset, token=os.environ.get(args.token_env, ""), api_base_url=args.api_base_url)
    except UploadError as exc:
        print(f"gitcode release assets: {exc}", file=sys.stderr)
        return 1
    print(json.dumps({"tag": args.tag, "assets": result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
