# Kubernetes Mutating Admission Webhook for pod modifications

This tutoral shows how to build and deploy a [MutatingAdmissionWebhook](https://kubernetes.io/docs/admin/admission-controllers/#mutatingadmissionwebhook-beta-in-19) that injects a nginx sidecar container into pod prior to persistence of the object.

## Prerequisites

Kubernetes 1.9.0 or above with the `admissionregistration.k8s.io/v1beta1` API enabled. Verify that by the following command:
```
$ kubectl api-versions | grep admissionregistration.k8s.io/v1beta1
```
The result should be:
```
admissionregistration.k8s.io/v1beta1
```

In addition, the `MutatingAdmissionWebhook` and `ValidatingAdmissionWebhook` admission controllers should be added and listed in the correct order in the admission-control flag of kube-apiserver.

## Build

1. Setup dep

   The repo uses [dep](https://github.com/golang/dep) as the dependency management tool for its Go codebase. Install `dep` by the following command:
```
$ go get -u github.com/golang/dep/cmd/dep
```

2. Build and push docker image
   
```
$ ./build
```

## Deploy

1. Create a signed cert/key pair and store it in a Kubernetes `secret` that will be consumed by pod-modifier deployment
```
$ ./deployment/webhook-create-signed-cert.sh \
    --service pod-modifier-webhook-svc \
    --secret pod-modifier-webhook-certs \
    --namespace default
```

2. Patch the `MutatingWebhookConfiguration` by set `caBundle` with correct value from Kubernetes cluster
```
$ cat deployment/mutatingwebhook.yaml | \
    deployment/webhook-patch-ca-bundle.sh > \
    deployment/mutatingwebhook-ca-bundle.yaml
```

3. Deploy resources
```
$ kubectl create -f deployment/deployment.yaml
$ kubectl create -f deployment/service.yaml
$ kubectl create -f deployment/mutatingwebhook-ca-bundle.yaml
```

## Verify

1. The pod-modifier inject webhook should be running
```
$ kubectl get pods
NAME                                                  READY     STATUS    RESTARTS   AGE
pod-modifier-webhook-deployment-bbb689d69-882dd   1/1       Running   0          5m
$ kubectl get deployment
NAME                                  DESIRED   CURRENT   UP-TO-DATE   AVAILABLE   AGE
pod-modifier-webhook-deployment   1         1         1            1           5m
```

2. Label the default namespace with `pod-modifier=enabled`
```
$ kubectl label namespace default pod-modifier=enabled
$ kubectl get namespace -L pod-modifier
NAME          STATUS    AGE       pod-modifier
default       Active    18h       enabled
kube-public   Active    18h
kube-system   Active    18h
```

3. Annotate your statefulset indicating which pod you want to modify and how
```
apiVersion: apps/v1beta1
kind: StatefulSet
metadata:
  :
spec:
  template:
    metadata:
         :
      annotations:
        pod-modifier.solace.com/inject: "true"
        pod-modifier.solace.com/modify.podDefinition: |
          {"Pods":[{"metadata":{"name":"{{ .Release.Name }}-pubsubplus-2"},"spec":{"containers": [{"name": "solace","resources": {"limits": {"cpu": "0.2","memory": "0.8Gi"},"requests": {"cpu": "0.5","memory": "2Gi"} }} ] } } ]}
    spec:
```

4. Verify actions of the pod modifier
```
AdmissionReview for Kind=/v1, Kind=Pod, Namespace=default Name= (test-run-solace-0) 
 Create patch for pod: test-run-solace-0/default
 Pod name is not matching annotation - skipping this pod.
AdmissionReview for Kind=/v1, Kind=Pod, Namespace=default Name= (test-run-solace-1) 
 Create patch for pod: test-run-solace-0/default
 Pod name is not matching annotation - skipping this pod.
AdmissionReview for Kind=/v1, Kind=Pod, Namespace=default Name= (test-run-solace-2) 
 Create patch for pod: test-run-solace-0/default
 AdmissionResponse: patch=[{"op":"replace","path":"/spec/containers/0/resources/requests/cpu","value":"500m"},{"op":"replace","path":"/spec/containers/0/resources/requests/memory","value":"2Gi"}]
```
