# singapore
kcp integration service

## Running the demo locally
See [README.md](./contrib/syncer-registration-demo/README.md)

## Building and deploying to a hub cluster
The following steps were vetted on macOS Big Sur.

1.  Export a registry where you have write access to push
```bash
export IMAGE_REGISTRY=quay.io/your_quay_username
```

2.  Install imagebuilder if not found on your machine
```bash
which imagebuilder
go install github.com/openshift/imagebuilder/cmd/imagebuilder@v1.2.1
```

3. Log in to your image registry
```bash
docker login quay.io -u your_quay_username -p your_quay_password
```

4. Build the image
```bash
make image-kcp-ocm-integration-controller
```

5. Push the image. Note your registry will need to be public or you will need to set up a pull secret for it on your hub cluster.
```bash
docker push $IMAGE_REGISTRY/kcp-ocm-integration-controller
```

6. Kustomize the deployment.yaml file to use your image
```bash
cd deploy/base
kustomize edit set image quay.io/skeeey/kcp-ocm-integration-controller:kcp-release-0.4=$IMAGE_REGISTRY/kcp-ocm-integration-controller:latest
cd ../..
```

7. Export environment variables for your hub and kcp kubeconfigs
```bash
export HUB_KUBECONFIG=~/.kube/config
export KCP_KUBECONFIG=/path/to/your/kcp.kubeconfig
```

8. Run `make deploy` to create the deployment on your hub cluster
```bash
make deploy
```

9. Test the integration by annotating a ManagedClusterSet. If the controller is working correctly, you should then see a SyncTarget created in your KCP workspace and a syncer deployed on the managed clusters in your managed cluster set.
```bash
kubectl annotate managedclusterset your_clusterset_name "kcp-workspace=root:org_name:ws_name"
```

## Customizing KCP Syncer

There are several environment variables that can be used in deploy/base/deployment.yaml for the `kcp-ocm-integration-controller` container.  To see the current environment variables in use:

```
oc get deployment kcp-ocm-integration-controller -n open-cluster-management  -o jsonpath='{.spec.template.spec.containers[0].env[*]}'
```

Environment variables:

- KCP_SYNCER_IMAGE

  Specify the docker image to use for the kcp syncer.  The default is `"quay.io/skeeey/kcp-syncer:release-0.4"`

- KCP_SYNCER_CRONJOB_SCHEDULE

  Specify the frequency, using cron schedule syntax, that the syncer deployment will perform a rolling restart.  By default, this CronJob is disabled if the environment variable is not defined.  This will allow the syncer to pick up the latest image when a floating tag is used. Typically only needed when using development/unstable branches.  Example value, every day at 02:00  `"0 2 * * *"`   


## Removing from a hub cluster

1. Remove the annotation from the managedclusterset
```bash
kubectl annotate managedclusterset your_clusterset_name kcp-workspace-
```

2. Run `make undeploy` to remove the deployment on your hub cluster
```bash
make undeploy
```
