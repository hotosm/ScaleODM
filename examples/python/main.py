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


def print_log_preview(uuid: str, line: int = 0, edge_lines: int = 20) -> None:
    """Fetch task logs and print only the first and last N logical log lines."""
    url = f"{BASE_URL}/task/{uuid}/output?line={line}"
    print(f"\nGET {url}")
    resp = requests.get(url, timeout=60)
    print(f"Status: {resp.status_code}")
    resp.raise_for_status()

    output = resp.text

    # /task/{uuid}/output may return either newline-delimited text or a JSON
    # array of log strings (NodeODM-style). Support both and normalize to lines.
    parsed_json = None
    try:
        parsed_json = resp.json()
    except ValueError:
        pass

    if isinstance(parsed_json, list) and all(isinstance(item, str) for item in parsed_json):
        lines = parsed_json
    elif isinstance(parsed_json, str):
        lines = parsed_json.splitlines()
    else:
        lines = output.splitlines()

    total = len(lines)

    print(f"Log lines available: {total}")
    print("--- log preview start ---")

    if total == 0:
        print("[no logs]")
    elif total <= edge_lines * 2:
        print("\n".join(lines))
    else:
        first = "\n".join(lines[:edge_lines])
        last = "\n".join(lines[-edge_lines:])
        print(first)
        print(f"... [omitted {total - (edge_lines * 2)} middle lines] ...")
        print(last)

    print("--- log preview end ---")


def _is_s3_not_found_xml(body: str) -> bool:
    """Return True when body is an S3 XML NoSuchKey/NotFound response."""
    try:
        root = ET.fromstring(body)
    except ET.ParseError:
        return False

    code_el = root.find(".//Code")
    code = (code_el.text or "").strip() if code_el is not None else ""
    return code in {"NoSuchKey", "NotFound", "NoSuchBucket"}


def validate_asset_exists(uuid: str, asset: str) -> None:
    """Check that a download asset is available without downloading full content."""
    url = f"{BASE_URL}/task/{uuid}/download/{asset}"
    print(f"\nGET {url} (no redirect follow)")
    resp = requests.get(url, allow_redirects=False, timeout=30)
    print(f"Status: {resp.status_code}")

    if resp.status_code not in (301, 302, 307, 308):
        print(f"Asset check failed for {asset}: expected redirect, got {resp.status_code}")
        print(f"Body: {resp.text}")
        return

    location = resp.headers.get("Location", "")
    if not location:
        print(f"Asset check failed for {asset}: missing redirect Location header")
        return

    print(f"Redirect URL present: {location[:120]}{'...' if len(location) > 120 else ''}")

    ranged = requests.get(
        location,
        headers={"Range": "bytes=0-0"},
        stream=True,
        timeout=30,
    )
    content_type = ranged.headers.get("Content-Type", "unknown")
    content_length = ranged.headers.get("Content-Length", "unknown")

    if ranged.status_code in (200, 206):
        print(
            f"Range GET status: {ranged.status_code} "
            f"content-length={content_length} content-type={content_type}"
        )
        print(f"Asset exists: {asset}")
    elif ranged.status_code == 404 and _is_s3_not_found_xml(ranged.text):
        print(
            f"Range GET status: 404 content-length={content_length} "
            f"content-type={content_type}"
        )
        print(f"Asset missing in S3: {asset}")
    else:
        print(
            f"Range GET status: {ranged.status_code} "
            f"content-length={content_length} content-type={content_type}"
        )
        print(f"Unexpected response while validating asset {asset}")

    ranged.close()

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
        print_log_preview(uuid)
        validate_asset_exists(uuid, "all.zip")
        validate_asset_exists(uuid, "orthophoto.tif")
    except Exception as exc:
        print(f"Error during follow-up calls: {exc}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
