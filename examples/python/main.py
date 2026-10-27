#!/usr/bin/env python3
"""
Simple Python script to exercise the ScaleODM NodeODM-compatible API.

This script:
  1. Creates a new task via POST /task/new
  2. Polls task status via GET /task/{uuid}/info until complete
  3. Lists tasks via GET /task/list
  4. Fetches final task info via GET /task/{uuid}/info

It is intended for local testing against a running ScaleODM instance.
"""

import json
import os
import sys
import time
import xml.etree.ElementTree as ET

import requests

# NodeODM-compatible status codes
STATUS_QUEUED = 10
STATUS_RUNNING = 20
STATUS_FAILED = 30
STATUS_COMPLETED = 40
STATUS_CANCELED = 50

TERMINAL_CODES = {STATUS_FAILED, STATUS_COMPLETED, STATUS_CANCELED}

BASE_URL = os.environ.get("SCALEODM_BASE_URL", "http://localhost:31100")


def create_task() -> str:
    """Create a new task using /task/new and return the task UUID."""
    s3_endpoint = os.environ.get("SCALEODM_WORKFLOW_S3_ENDPOINT", "http://host.docker.internal:31102")

    read_s3_path = "s3://scaleodm-test/test/"
    write_s3_path = "s3://scaleodm-test/test/output/"

    options = [
        {"name": "fast-orthophoto", "value": True},
    ]

    payload = {
        "name": "scaleodm-test-project",
        "readS3Path": read_s3_path,
        "writeS3Path": write_s3_path,
        "s3Endpoint": s3_endpoint,
        "s3Region": "us-east-1",
        "processingMode": "standard",
        "s3ScanDepth": 1,
        "options": json.dumps(options),
    }

    url = f"{BASE_URL}/task/new"
    print(f"POST {url}")
    resp = requests.post(url, json=payload, timeout=30)
    print(f"Status: {resp.status_code}")
    print(f"Body: {resp.text}")

    resp.raise_for_status()
    data = resp.json()

    uuid = data.get("uuid")
    if not uuid:
        raise RuntimeError(f"Could not find task UUID in response: {data!r}")

    print(f"Created task with UUID: {uuid}")
    uuid_file = os.environ.get("SCALEODM_EXAMPLE_UUID_FILE")
    if uuid_file:
        with open(uuid_file, "w", encoding="utf-8") as f:
            f.write(uuid)
    return uuid


def list_tasks() -> None:
    """Call GET /task/list and print the response."""
    url = f"{BASE_URL}/task/list"
    print(f"\nGET {url}")
    resp = requests.get(url, timeout=30)
    print(f"Status: {resp.status_code}")
    print(f"Body: {resp.text}")


def task_info(uuid: str) -> dict:
    """Call GET /task/{uuid}/info and return the response data."""
    url = f"{BASE_URL}/task/{uuid}/info"
    print(f"\nGET {url}")
    resp = requests.get(url, timeout=30)
    print(f"Status: {resp.status_code}")
    print(f"Body: {resp.text}")
    resp.raise_for_status()
    return resp.json()


def print_log_summary(uuid: str, line: int = 0) -> None:
    """Fetch task logs and print a one-line summary plus a command to view them."""
    url = f"{BASE_URL}/task/{uuid}/output?line={line}"
    resp = requests.get(url, timeout=60)
    resp.raise_for_status()

    parsed_json = None
    try:
        parsed_json = resp.json()
    except ValueError:
        pass

    if isinstance(parsed_json, list) and all(isinstance(item, str) for item in parsed_json):
        total = len(parsed_json)
    elif isinstance(parsed_json, str):
        total = len(parsed_json.splitlines())
    else:
        total = len(resp.text.splitlines())

    print(f"\nWorkflow logs: {total} lines available")
    print(f"To view full logs run: curl '{url}'")


def _is_s3_not_found_xml(body: str) -> bool:
    """Return True when body is an S3 XML NoSuchKey/NotFound response."""
    try:
        root = ET.fromstring(body)
    except ET.ParseError:
        return False

    code_el = root.find(".//Code")
    code = (code_el.text or "").strip() if code_el is not None else ""
    return code in {"NoSuchKey", "NotFound", "NoSuchBucket"}


def _short_body(resp: "requests.Response", limit: int = 300) -> str:
    """Return a short, terminal-safe snippet of a response body.

    Guards against dumping binary asset content (e.g. a streamed all.zip) to
    the terminal. Only textual content types are shown; anything else is
    summarised by size.
    """
    content_type = resp.headers.get("Content-Type", "")
    if not any(t in content_type for t in ("json", "text", "xml")):
        length = resp.headers.get("Content-Length", "?")
        return f"<{length} bytes of {content_type or 'binary'} omitted>"
    text = resp.text
    return text if len(text) <= limit else text[:limit] + "…"


def _validate_ranged(asset: str, location: str) -> bool:
    """Confirm a redirect target is fetchable via a 1-byte ranged GET."""
    ranged = requests.get(
        location,
        headers={"Range": "bytes=0-0"},
        stream=True,
        timeout=30,
    )
    try:
        content_type = ranged.headers.get("Content-Type", "unknown")
        content_length = ranged.headers.get("Content-Length", "unknown")
        prefix = f"Range GET status: {ranged.status_code} content-length={content_length} content-type={content_type}"

        if ranged.status_code in (200, 206):
            print(prefix)
            print(f"Asset exists: {asset}")
            return True
        if ranged.status_code == 404 and _is_s3_not_found_xml(ranged.text):
            print(prefix)
            print(f"Asset missing in S3: {asset}")
            return False
        print(prefix)
        print(f"Unexpected response while validating asset {asset}")
        return False
    finally:
        ranged.close()


def validate_asset_exists(uuid: str, asset: str) -> bool:
    """Check that a download asset is available without downloading full content.

    The download endpoint has two success shapes:
      - a 3xx redirect to a pre-signed S3 URL (real object on disk), or
      - a direct 200 stream (synthetic all.zip assembled on the fly).
    Both count as "available".
    """
    url = f"{BASE_URL}/task/{uuid}/download/{asset}"
    print(f"\nGET {url} (no redirect follow)")
    # stream=True so we never pull a large (possibly binary) body into memory
    # just to inspect status/headers.
    resp = requests.get(url, allow_redirects=False, stream=True, timeout=30)
    try:
        status = resp.status_code
        content_type = resp.headers.get("Content-Type", "unknown")
        print(f"Status: {status} content-type={content_type}")

        # Real object in S3: redirect to a pre-signed URL.
        if status in (301, 302, 307, 308):
            location = resp.headers.get("Location", "")
            if not location:
                print(f"Asset check FAILED for {asset}: redirect without Location header")
                return False
            print(f"Redirect -> {location[:120]}{'…' if len(location) > 120 else ''}")
            return _validate_ranged(asset, location)

        # Synthetic all.zip: streamed directly rather than redirected.
        if status == 200:
            length = resp.headers.get("Content-Length", "unknown")
            print(f"Asset streamed directly: {asset} content-length={length}")
            return True

        if status == 404:
            print(f"Asset MISSING for {asset}: {_short_body(resp)}")
            return False

        print(f"Asset check FAILED for {asset}: unexpected status {status}: {_short_body(resp)}")
        return False
    finally:
        resp.close()

def wait_for_task(uuid: str, timeout: int = 7200, interval: int = 60) -> None:
    """
    Poll /task/{uuid}/info until the task reaches a terminal state or timeout.

    Status codes: 10=QUEUED, 20=RUNNING, 30=FAILED, 40=COMPLETED, 50=CANCELED
    """
    deadline = time.time() + timeout

    while time.time() < deadline:
        url = f"{BASE_URL}/task/{uuid}/info"
        resp = requests.get(url, timeout=30)
        resp.raise_for_status()
        data = resp.json()

        status = data.get("status", {})
        code = status.get("code")
        error_msg = status.get("errorMessage", "")
        print(f"Task {uuid} status code: {code}")

        if code in TERMINAL_CODES:
            if code == STATUS_COMPLETED:
                print(f"Task {uuid} completed successfully")
                return
            elif code == STATUS_FAILED:
                raise RuntimeError(
                    f"Task failed: {error_msg or 'unknown error'}"
                )
            elif code == STATUS_CANCELED:
                raise RuntimeError("Task was canceled")

        time.sleep(interval)

    raise TimeoutError(
        f"Task {uuid} did not reach terminal state in {timeout}s"
    )


def main() -> None:
    print(f"Using ScaleODM API at: {BASE_URL}")

    try:
        uuid = create_task()
    except Exception as exc:
        print(f"Failed to create task: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        wait_for_task(uuid)
    except Exception as exc:
        print(f"Task did not complete successfully: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        list_tasks()
        info = task_info(uuid)
        print("\nFinal task summary:")
        print(json.dumps(info, indent=2))
        print_log_summary(uuid)
        # "orthophoto" is the alias the download endpoint resolves to the real
        # object key (odm_orthophoto/odm_orthophoto.tif); the bare "orthophoto.tif"
        # key does not exist. "all.zip" is streamed synthetically.
        results = {
            asset: validate_asset_exists(uuid, asset)
            for asset in ("all.zip", "orthophoto")
        }
    except Exception as exc:
        print(f"Error during follow-up calls: {exc}", file=sys.stderr)
        sys.exit(1)

    missing = [asset for asset, ok in results.items() if not ok]
    if missing:
        print(f"\nExpected assets not available: {', '.join(missing)}", file=sys.stderr)
        sys.exit(1)
    print(f"\nAll expected assets available: {', '.join(results)}")


if __name__ == "__main__":
    main()
