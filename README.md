# Progressive Rollout Controller
A kubernetes controller for managing progressive rollouts of Daemonsets.

## Overview
Deploying new DaemonSets to a Kubernetes cluster typically results in pods being 
simultaneously scheduled across all nodes which can lead to thundering herd.
The Progressive Rollout Controller mitigates these risks by rolling out resources incrementally, 
allowing for careful monitoring of unexpected behavior and resource usage.

## Running Locally with Kind
Follow these steps to run the Progressive Rollout Controller locally using Kind
1. Install Kind:
```bash
brew install kind
```
2. Set up a Kind cluster:
This will create the cluster, load necessary images, and apply relevant configurations.
```bash
make kind
```
3. Apply Daemonset:
```bash
kubectl apply -f examples/manifests/test-daemonset.yaml -n progressive-rollout-controller
```
4. Watch the rollout:
```bash
kubectl get pods -n progressive-rollout-controller -w
```