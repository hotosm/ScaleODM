# Use AWS STS Temporary Credentials for S3 Access in Argo Workflows

## Context and Problem Statement

Each ODM (OpenDroneMap) job in ScaleODM needs to download raw imagery from S3
and upload processed outputs back to S3. The system uses Argo Workflows for
long-running processing jobs, often lasting hours or days, across potentially
federated clusters connected via a mesh VPN.

Initially, S3 credentials were injected into the workflow environment
(via env vars or Kubernetes secrets). This created several issues:

- Credentials proliferated across clusters in a federated setup.
- Secrets could be exposed through logs or stored in etcd.
- Key rotation and lifecycle management were difficult.
- Pre-signed URLs were considered but deemed unscalable for hundreds to
  thousands of files per job and unsuitable for large datasets due to I/O
  overhead generating thousands of pre-signed URLs.
- Zipping all the data is also not ideal, as it required pre-processing
  into a zip, and makes the imagery less accessible, e.g. for viewing
  and validation in a frontend.

We need a solution that:

- Provides temporary and scoped S3 access per job.
- Works securely across federated ScaleODM instances.
- Avoids storing long-lived credentials in Kubernetes.
- Minimises operational complexity and avoids zipping imagery for I/O efficiency.

## Considered Options

- Use long-lived static AWS credentials in Kubernetes Secrets.
- Use short-lived AWS STS credentials per workflow.
- Use pre-signed URLs for each job's S3 read/write operations.

## Decision Outcome

We chose to use **short-lived AWS STS temporary credentials** for all ODM
job data transfers (imagery download and product upload).

STS temporary credentials are issued per job by the ScaleODM control plane
and provide tightly scoped access to specific S3 bucket prefixes.
This approach avoids the granularity issues of pre-signed URLs,
supports large datasets without zipping, and ensures secure access
across federated clusters.

Each job submission will:

1. Use the ScaleODM Go API to assume an IAM role via `sts:AssumeRole`,
   generating temporary credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
   `AWS_SESSION_TOKEN`) scoped to the job's S3 prefix.
2. Inject these credentials as environment variables into the Argo Workflow CRD.
3. Allow the workflow to access S3 directly using standard AWS SDKs or
   tools (e.g., `aws s3 cp`).

Example IAM role trust policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "AWS": "arn:aws:iam::ACCOUNT_ID:user/scaleodm-admin"
      },
      "Action": "sts:AssumeRole"
    }
  ]
}

Example Argo workflow vars:

```yaml
env:
  - name: AWS_ACCESS_KEY_ID
    value: "<temporary_access_key>"
  - name: AWS_SECRET_ACCESS_KEY
    value: "<temporary_secret_key>"
  - name: AWS_SESSION_TOKEN
    value: "<temporary_session_token>"
  - name: S3_INPUT_PREFIX
    value: "s3://bucket/job123/input/"
  - name: S3_OUTPUT_PREFIX
    value: "s3://bucket/job123/output/"
```

## Consequences

- ✅ No persistent AWS credentials are stored in the cluster.
- ✅ Works transparently across federated clusters with Tailscale.
- ✅ Temporary credentials automatically expire (e.g., 12–24h) and are tightly 
  scoped to job-specific S3 prefixes.
- ✅ Supports large datasets and numerous files without zipping or per-file URL generation.
- ❌ Requires a centralised ScaleODM API to manage sts:AssumeRole securely.
- ❌ Adds minor complexity for the user having to configure an IAM user for STS.
