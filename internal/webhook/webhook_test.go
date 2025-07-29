package webhook_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reddit/progressivedaemonset/internal/config"
	"github.com/reddit/progressivedaemonset/internal/webhook"

	"go.uber.org/zap"

	"k8s.io/apimachinery/pkg/runtime"

	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	schedulingGateName = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
	progressiveLabel   = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
)

func TestServeDaemonsetMutate(t *testing.T) {
	log, err := zap.NewProduction()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	logger := log.Sugar()
	defer logger.Sync()

	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-daemonset",
			Namespace: "default",
			Labels:    map[string]string{"app": "my-daemonset"},
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "my-daemonset"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
		},
	}

	rawDS, err := json.Marshal(ds)
	if err != nil {
		t.Fatalf("failed to marshal daemonset: %v", err)
	}

	client, ts, payload := makeClientAndServerHelper(rawDS, true)
	defer ts.Close()

	resp, err := client.Post(ts.URL+"/daemonset/mutate", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	var admissionReviewResp admissionv1.AdmissionReview
	if err := json.Unmarshal(bodyBytes, &admissionReviewResp); err != nil {
		t.Fatalf("failed to unmarshal response admission review: %v", err)
	}

	if !admissionReviewResp.Response.Allowed {
		t.Fatalf("expected allowed response, got: %v", admissionReviewResp.Response.Result)
	}

	var patches []map[string]interface{}
	if err := json.Unmarshal(admissionReviewResp.Response.Patch, &patches); err != nil {
		t.Fatalf("failed to unmarshal patch: %v", err)
	}

	labelFound := false
	expectedLabel := progressiveLabel
	expectedValue := "true"

	for _, patch := range patches {
		path, ok := patch["path"].(string)
		if !ok {
			continue
		}
		// need to replace "/" with "~1" in the path for JSON Patch format
		if (strings.Contains(path, "/metadata/labels/"+strings.Replace(expectedLabel, "/", "~1", -1)) &&
			strings.Contains(path, "/spec/template/metadata/labels/"+strings.Replace(expectedLabel, "/", "~1", -1))) &&
			patch["op"].(string) == "add" && patch["value"].(string) == expectedValue {
			labelFound = true
			break
		}
	}

	if !labelFound {
		t.Fatalf("expected patch to contain the label '%s: %s', got: %s",
			expectedLabel, expectedValue, string(admissionReviewResp.Response.Patch))
	}
}

func TestServePodMutate(t *testing.T) {
	log, err := zap.NewProduction()
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	logger := log.Sugar()
	defer logger.Sync()

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "busybox",
					Image:   "busybox",
					Command: []string{"sleep", "3600"},
				},
			},
			SchedulingGates: []corev1.PodSchedulingGate{},
		},
	}

	rawPod, err := json.Marshal(pod)
	if err != nil {
		t.Fatalf("failed to marshal pod: %v", err)
	}

	client, ts, payload := makeClientAndServerHelper(rawPod, false)
	defer ts.Close()

	resp, err := client.Post(ts.URL+"/pod/mutate", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	var admissionReviewResp admissionv1.AdmissionReview
	if err := json.Unmarshal(bodyBytes, &admissionReviewResp); err != nil {
		t.Fatalf("failed to unmarshal response admission review: %v", err)
	}

	if !admissionReviewResp.Response.Allowed {
		t.Fatalf("expected allowed response, got: %v", admissionReviewResp.Response.Result)
	}

	var patches []map[string]interface{}
	if err = json.Unmarshal(admissionReviewResp.Response.Patch, &patches); err != nil {
		t.Fatalf("failed to unmarshal patches: %v", err)
	}

	gateFound := false
	for _, patch := range patches {
		path, _ := patch["path"].(string)
		op, _ := patch["op"].(string)

		if path == "/spec/schedulingGates" && op == "add" {
			if values, ok := patch["value"].([]interface{}); ok {
				for _, gate := range values {
					if gateObj, ok := gate.(map[string]interface{}); ok {
						if name, ok := gateObj["name"].(string); ok &&
							name == schedulingGateName {
							gateFound = true
							break
						}
					}
				}
			}
		}
	}

	if !gateFound {
		t.Fatalf("expected patch to add scheduling gate with name '%s', got: %s",
			schedulingGateName, string(admissionReviewResp.Response.Patch))
	}
}

func makeClientAndServerHelper(rawObj []byte, addLabelOnCreate bool) (*http.Client, *httptest.Server, []byte) {
	log, _ := zap.NewProduction()
	logger := log.Sugar()
	defer logger.Sync()

	ar := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "test-uid",
			Operation: admissionv1.Create,
			Object:    runtime.RawExtension{Raw: rawObj},
		},
	}
	payload, err := json.Marshal(ar)
	if err != nil {
		logger.Fatalf("failed to marshal admission review: %v", err)
	}
	opts := config.Options{
		RolloutInterval: 5 * time.Minute,
	}
	cfg := &config.Config{
		DaemonsetOptions:         &opts,
		Logger:                   logger,
		Clientset:                nil,
		AutoIncludeNewDaemonsets: addLabelOnCreate,
	}

	mux := webhook.NewWebhookServer(cfg)
	ts := httptest.NewTLSServer(mux)

	client := ts.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
	}
	return client, ts, payload
}
