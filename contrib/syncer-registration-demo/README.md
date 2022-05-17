# Run the KCP and OCM integration demo

1. Setup a 1 hub/2 cluster environment with the cluster name as cluster1 and cluster2. Use script [here](https://github.com/open-cluster-management-io/OCM/tree/main/solutions/setup-dev-environment) to setup on kind

2. Setup demo enviroment
    - run `./startKCPServer.sh` to start a KCP server
    - run `./startIntegrationController.sh` to start the kcp-ocm integration controller in another terminal. You may need to `export LISTEN=0.0.0.0:8449` e.g. if your port 8443 is already in use

3. Run the demo script `./demo-script.sh` in aother terminal

4. Run the `./demo-clean.sh` to clean up the demo enviroment
