FROM alpine:latest

ADD k8s-mutating-statefulset-webhook /k8s-mutating-statefulset-webhook
ENTRYPOINT ["./k8s-mutating-statefulset-webhook"]