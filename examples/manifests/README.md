# Manual Argo Workflows

- While the ScaleODM app orchestrates Argo Workflows,
  it's also possible to manually trigger processing.
- We can defined our own Argo manifests, to trigger
  a processing pipelines for our imagery.

## Standard Processing

1. Ensure Argo Workflows is installed first:
   https://argo-workflows.readthedocs.io/en/latest/installation

2. Modify the YAML to your needs.

3. Create the workflow:

```bash
kubectl create -f standard.yaml
```
