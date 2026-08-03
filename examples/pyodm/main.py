#!/usr/bin/env python3
"""
ScaleODM example using the pyodm SDK.

pyodm's create_task() uses NodeODM's chunked upload which ScaleODM doesn't
implement (images are already in S3). Instead, use Node.post() for task
creation and the standard Task class for monitoring/downloads.

This script:
  1. Checks node info via pyodm
  2. Creates a task via POST /task/new (JSON) to ScaleODM
  3. Monitors the task via Task.info() / Task.wait_for_completion()
"""

import json
import os
import sys
import xml.etree.ElementTree as ET

import requests
from pyodm import Node
from pyodm.api import Task

HOST = os.environ.get("SCALEODM_HOST", "localhost")
PORT = int(os.environ.get("SCALEODM_PORT", "31100"))


def create_s3_task(
    node,
    api_base_url,
    read_s3_path,
    write_s3_path,
    s3_endpoint,
    name="odm-project",
    options=None,
    processing_mode="standard",
    s3_scan_depth=1,
):
    """Create a ScaleODM task from an S3 path of images.

    Args:
        node: pyodm Node pointed at ScaleODM
        read_s3_path: S3 path containing images (e.g. "s3://bucket/images/")
        write_s3_path: S3 path where outputs are written
        s3_endpoint: S3-compatible endpoint URL for workflow operations
        name: Project name
        options: Dict of ODM options (e.g. {"dsm": True})
        processing_mode: ScaleODM pipeline mode (default "standard").
        s3_scan_depth: Max depth for the rclone scan beneath read_s3_path
            (default 1 - only the given dir; raise for multi-task layouts
            like projectid/taskid/images).

    Returns:
        pyodm Task object
    """
    odm_options = []
    if options:
        for k, v in options.items():
            odm_options.append({"name": k, "value": v})

    data = {
        "name": name,
        "readS3Path": read_s3_path,
        "writeS3Path": write_s3_path,
        "s3Endpoint": s3_endpoint,
        "s3Region": "us-east-1",
        "processingMode": processing_mode,
        "s3ScanDepth": s3_scan_depth,
    }
    if odm_options:
        data["options"] = json.dumps(odm_options)

    response = requests.post(f"{api_base_url}/task/new", json=data, timeout=30)
    response.raise_for_status()
    uuid = response.json()["uuid"]
    print(f"Created task: {uuid}")
    uuid_file = os.environ.get("SCALEODM_EXAMPLE_UUID_FILE")
    if uuid_file:
        with open(uuid_file, "w", encoding="utf-8") as f:
            f.write(uuid)
    return Task(node, uuid)


def print_log_summary(api_base_url: str, uuid: str, line: int = 0) -> None:
    """Fetch task logs and print a one-line summary plus a command to view them."""
    url = f"{api_base_url}/task/{uuid}/output?line={line}"
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


def validate_asset_exists(api_base_url: str, uuid: str, asset: str) -> bool:
    """Check that a download asset is available without downloading full content.

    The download endpoint has two success shapes:
      - a 3xx redirect to a pre-signed S3 URL (real object on disk), or
      - a direct 200 stream (synthetic all.zip assembled on the fly).
    Both count as "available".
    """
    url = f"{api_base_url}/task/{uuid}/download/{asset}"
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

def main() -> None:
    node = Node(HOST, PORT)

    # Verify connectivity
    info = node.info()
    print(f"Connected to ScaleODM {info.version}")
    print(f"Engine: {info.engine} {info.engine_version}")
    print(f"Queue: {info.task_queue_count} tasks")

    api_base_url = f"http://{HOST}:{PORT}"
    read_s3_path = "s3://scaleodm-test/test/"
    write_s3_path = "s3://scaleodm-test/test/output/"
    s3_endpoint = os.environ.get("SCALEODM_WORKFLOW_S3_ENDPOINT", "http://host.docker.internal:31102")
    print(f"\nCreating task from: {read_s3_path}")
    print(f"Writing outputs to: {write_s3_path}")
    print(f"Using S3 endpoint: {s3_endpoint}")

    task = create_s3_task(
        node,
        api_base_url,
        read_s3_path,
        write_s3_path,
        s3_endpoint,
        name="pyodm-test-project",
        options={"fast-orthophoto": True},
        processing_mode="standard",
        s3_scan_depth=1,
    )

    # Monitor via polling
    def on_status(info):
        print(f"  Status: {info.status.name} ({info.progress}%)")

    print("Waiting for task to complete...")
    try:
        task.wait_for_completion(status_callback=on_status)
    except Exception as exc:
        print(f"Task failed: {exc}", file=sys.stderr)
        sys.exit(1)

    # Final info
    info = task.info()
    print(f"\nTask completed!")
    print(f"  UUID: {info.uuid}")
    print(f"  Status: {info.status.name}")
    print(f"  Processing time: {info.processing_time}ms")

    info_resp = requests.get(f"{api_base_url}/task/{info.uuid}/info", timeout=30)
    info_resp.raise_for_status()
    print("\nFinal task summary:")
    print(json.dumps(info_resp.json(), indent=2))

    print_log_summary(api_base_url, info.uuid)
    # "orthophoto" is the alias the download endpoint resolves to the real
    # object key (odm_orthophoto/odm_orthophoto.tif); the bare "orthophoto.tif"
    # key does not exist. "all.zip" is streamed synthetically.
    results = {
        asset: validate_asset_exists(api_base_url, info.uuid, asset)
        for asset in ("all.zip", "orthophoto")
    }
    missing = [asset for asset, ok in results.items() if not ok]
    if missing:
        print(f"\nExpected assets not available: {', '.join(missing)}", file=sys.stderr)
        sys.exit(1)
    print(f"\nAll expected assets available: {', '.join(results)}")


if __name__ == "__main__":
    main()
