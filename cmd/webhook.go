package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	glog "github.com/golang/glog"
	"github.com/mattbaird/jsonpatch"
	"k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()

	// (https://github.com/kubernetes/kubernetes/issues/57982)
	defaulter = runtime.ObjectDefaulter(runtimeScheme)
)

var ignoredNamespaces = []string{
	metav1.NamespaceSystem,
	metav1.NamespacePublic,
}

const (
	admissionWebhookAnnotationInjectKey = "pod-modifier-webhook.solace.com/inject"
	admissionWebhookAnnotationStatusKey = "pod-modifier-webhook.solace.com/status"
)

type WebhookServer struct {
	server *http.Server
}

// Webhook Server parameters
type WhSvrParameters struct {
	port     int    // webhook server port
	certFile string // path to the x509 certificate for https
	keyFile  string // path to the x509 private key matching `CertFile`
}

// Check whether the target resoured need to be mutated
func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernete system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			glog.Infof("Skip mutation for %v for it' in special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}
	return true
}

// main mutation process
func (whsvr *WebhookServer) mutate(ar *v1.AdmissionReview) *v1.AdmissionResponse {
	req := ar.Request
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		glog.Errorf("Could not unmarshal raw object: %v", err)
		return &v1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pod.Name, req.UID, req.Operation, req.UserInfo)

	// determine whether to perform mutation
	if !mutationRequired(ignoredNamespaces, &pod.ObjectMeta) {
		glog.Infof("Skipping mutation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1.AdmissionResponse{
			Allowed: true,
		}
	}

	patchBytes, err := createPatch(&pod)
	if err != nil {
		return &v1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1.PatchType {
			pt := v1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

func createPatch(pod *corev1.Pod) ([]byte, error) {
	glog.Infof("Create patch for pod: %s/%s", pod.Name, pod.Namespace)

	initializedPod := pod.DeepCopy()

	a := pod.ObjectMeta.GetAnnotations()
	podDefinitionAnnotation, ok := a[annotation+".podDefinition"]

	if !ok {
		glog.Infof("Required '%s' annotation missing; skipping pod", annotation+".podDefinition")
		return []byte{}, nil
	}

	var c config
	err := json.Unmarshal([]byte(podDefinitionAnnotation), &c)
	if err != nil {
		glog.Errorf("Unmarshal failed err %v  ,  Annotation %s", err, podDefinitionAnnotation)
		return []byte{}, err
	}

	var cpod corev1.Pod
	found := false
	for _, cpod = range c.Pods {
		if pod.ObjectMeta.Name == cpod.ObjectMeta.Name {
			found = true
			break
		}
	}

	if !found {
		glog.Infof("Pod name is not matching annotation - skipping this pod.")
		return []byte{}, nil
	}

	// Modify the containers resources, if the container name of the specification matches
	// the conainer name of the "initialized pod container name"
	// Then patch the original pod
	found = false
	for _, configContainer := range cpod.Spec.Containers {
		for ii, initializedContainer := range initializedPod.Spec.Containers {
			if configContainer.Name == initializedContainer.Name {
				initializedPod.Spec.Containers[ii].Resources = configContainer.Resources
				found = true
			}
		}
	}
	if !found {
		glog.Infof("No container name is matching annotation - skipping this pod.")
		return []byte{}, nil
	}

	oldData, err := json.Marshal(pod)
	if err != nil {
		glog.Error(err)
		return []byte{}, err
	}

	newData, err := json.Marshal(initializedPod)
	if err != nil {
		glog.Error(err)
		return []byte{}, err
	}
	patch, err := jsonpatch.CreatePatch(oldData, newData)
	if err != nil {
		glog.Error(err)
		return []byte{}, err
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		glog.Error(err)
		return []byte{}, err
	}

	return patchBytes, nil
}

// Serve method for webhook server
func (whsvr *WebhookServer) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		glog.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("Content-Type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1.AdmissionResponse
	ar := v1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = whsvr.mutate(&ar)
	}

	admissionReview := v1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		glog.Errorf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}
	glog.Infof("Ready to write reponse ...")
	if _, err := w.Write(resp); err != nil {
		glog.Errorf("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
