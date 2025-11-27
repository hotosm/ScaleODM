#!/usr/bin/env python3
"""
Simple Python script to exercise the ScaleODM NodeODM-compatible API.

This script:
  1. Creates a new task via POST /task/new
  2. Lists tasks via GET /task/list
  3. Fetches task info via GET /task/{uuid}/info

It is intended for local testing against a running ScaleODM instance.
"""

import json
import os
import sys
import time
from typing import Any, Dict

import requests


BASE_URL = os.environ.get("SCALEODM_BASE_URL", "http://localhost:31100")


def create_task() -> str:
    """
    Create a new task using /task/new and return the task UUID.

    Required environment variables (if you use authenticated buckets):
      - SCALEODM_S3_ACCESS_KEY
      - SCALEODM_S3_SECRET_KEY
    """
    s3_access_key = os.environ.get("SCALEODM_S3_ACCESS_KEY", "")
    s3_secret_key = os.environ.get("SCALEODM_S3_SECRET_KEY", "")
    s3_endpoint = os.environ.get("SCALEODM_S3_ENDPOINT", "")

    read_s3_path = "s3://drone-tm-public/dtm-data/test/"
    # Allow the API to set this automatically: read_s3_path + 'output/'
    # write_s3_path = "s3://drone-tm-public/dtm-data/test/output/"

    options = [
        {"name": "fast-orthophoto", "value": True},
    ]

    payload: Dict[str, Any] = {
        "name": "scaleodm-test-project",
        "readS3Path": read_s3_path,
        # The Go API expects options as a JSON-encoded string
        "options": json.dumps(options),
    }

    # Only send explicit credentials/endpoint if configured; otherwise the server
    # can fall back to its own configuration and environment variables.
    if s3_access_key and s3_secret_key:
        payload["s3AccessKeyID"] = s3_access_key
        payload["s3SecretAccessKey"] = s3_secret_key
    if s3_endpoint:
        payload["s3Endpoint"] = s3_endpoint

    url = f"{BASE_URL}/task/new"
    print(f"POST {url}")
    resp = requests.post(url, json=payload, timeout=30)
    print(f"Status: {resp.status_code}")
    print(f"Body: {resp.text}")

    resp.raise_for_status()
    data = resp.json()

    # The API returns either {"uuid": "..."} directly or wrapped; tests
    # assume the direct form, so follow that here.
    uuid = data.get("uuid")
    if not uuid and isinstance(data, dict) and "body" in data:
        uuid = data["body"].get("uuid")

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


def task_info(uuid: str) -> None:
    """Call GET /task/{uuid}/info and print the response."""
    url = f"{BASE_URL}/task/{uuid}/info"
    print(f"\nGET {url}")
    resp = requests.get(url, timeout=30)
    print(f"Status: {resp.status_code}")
    print(f"Body: {resp.text}")


def wait_for_task(uuid: str, timeout: int = 3600, interval: int = 60) -> None:
    """
    Poll /task/{uuid}/info until the task reaches a terminal state or timeout.

    Terminal states are based on NodeODM/ScaleODM semantics.
    We wait 1hr for the job to complete by default.
    """
    deadline = time.time() + timeout

    while time.time() < deadline:
        url = f"{BASE_URL}/task/{uuid}/info"
        resp = requests.get(url, timeout=30)
        resp.raise_for_status()
        data = resp.json()

        status = data.get("status")
        print(f"Task {uuid} status: {status!r}")

        if status in {"FINISHED", "ERROR", "CANCELED"}:
            if status != "FINISHED":
                raise RuntimeError(f"Task did not finish successfully: {status!r}")
            print(f"Task {uuid} finished successfully")
            return

        time.sleep(interval)

    raise TimeoutError(f"Task {uuid} did not reach terminal state in {timeout} seconds")


def main() -> None:
    print(f"Using ScaleODM API at: {BASE_URL}")

    try:
        uuid = create_task()
    except Exception as exc:
        print(f"Failed to create task: {exc}", file=sys.stderr)
        sys.exit(1)

    # Wait for the workflow to reach a terminal state before follow-up calls.
    try:
        wait_for_task(uuid)
    except Exception as exc:
        print(f"Task did not complete successfully: {exc}", file=sys.stderr)
        sys.exit(1)

    try:
        list_tasks()
        task_info(uuid)
    except Exception as exc:
        print(f"Error during follow-up calls: {exc}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
