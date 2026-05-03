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
    return Task(node, uuid)


def print_log_preview(api_base_url: str, uuid: str, line: int = 0, edge_lines: int = 20) -> None:
    """Fetch task logs and print only the first and last N logical log lines."""
    url = f"{api_base_url}/task/{uuid}/output?line={line}"
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


def validate_asset_exists(api_base_url: str, uuid: str, asset: str) -> None:
    """Check that a download asset is available without downloading full content."""
    url = f"{api_base_url}/task/{uuid}/download/{asset}"
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

    print_log_preview(api_base_url, info.uuid)
    validate_asset_exists(api_base_url, info.uuid, "all.zip")
    validate_asset_exists(api_base_url, info.uuid, "orthophoto.tif")


if __name__ == "__main__":
    main()
