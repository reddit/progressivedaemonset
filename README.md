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
```
3. Apply DaemonSet:
```console
$ kubectl apply -f examples/manifests/test-daemonset.yaml -n progressivedaemonset
```
4. Watch the rollout:
```console
$ kubectl get pods -n progressivedaemonset -w
```