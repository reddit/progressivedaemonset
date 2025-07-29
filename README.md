# ProgressiveDaemonSet
A kubernetes controller for managing progressive rollouts of *new* Daemonsets.

## Overview
Deploying new DaemonSets to a Kubernetes cluster typically results in pods being 
simultaneously scheduled across all nodes which can lead to thundering herd.
ProgressiveDaemonSet mitigates these risks by rolling out resources incrementally, 
allowing for careful monitoring of unexpected behavior and resource usage.

## Running Locally with Kind
Follow these steps to run the ProgressiveDaemonset Controller locally using Kind
1. Install Kind:
```console
$ brew install kind
```
2. Set up a Kind cluster:
This will create the cluster, load necessary images, and apply relevant configurations.
```console
$ make kind

# progressivedaemonset controller will be running in the `progressivedaemonset` namespace.
$ kubectl get pods -n progressivedaemonset
NAME                                    READY   STATUS    RESTARTS   AGE
progressivedaemonset-6957f78945-6v29m   1/1     Running   0          10m

```
3. Apply DaemonSet:
```console
$ kubectl apply -f examples/manifests/test-daemonset.yaml -n progressivedaemonset
```
4. Watch the rollout:
```console
$ kubectl get pods -n progressivedaemonset -w
# progressivedaemonset controller removes scheduling gates on the 4 DaemonSet pods incrementally, 
# configured to every 10 seconds in this example (see `examples/manifests/test-daemonset.yaml`).
NAME                                    READY   STATUS    RESTARTS   AGE
progressivedaemonset-6957f78945-6v29m   1/1     Running   0          10m
test-daemonset-qpv5t                    0/1     SchedulingGated   0          0s
test-daemonset-zmldf                    0/1     SchedulingGated   0          0s
test-daemonset-hwj5m                    0/1     SchedulingGated   0          0s
test-daemonset-czqfp                    0/1     SchedulingGated   0          0s

test-daemonset-qpv5t                    0/1     Pending           0            1s
test-daemonset-qpv5t                    0/1     ContainerCreating   0          1s
test-daemonset-qpv5t                    1/1     Running             0          4s
test-daemonset-zmldf                    0/1     Pending             0          11s
test-daemonset-zmldf                    0/1     ContainerCreating   0          11s
test-daemonset-zmldf                    1/1     Running             0          14s
test-daemonset-hwj5m                    0/1     Pending             0          21s
test-daemonset-hwj5m                    0/1     ContainerCreating   0          21s
test-daemonset-hwj5m                    1/1     Running             0          24s
test-daemonset-czqfp                    0/1     Pending             0          31s
test-daemonset-czqfp                    0/1     ContainerCreating   0          31s
test-daemonset-czqfp                    1/1     Running             0          34s

```