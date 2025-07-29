package webhook

import (
	"encoding/json"
	"io"
	"net/http"

	c "github.com/reddit/progressivedaemonset/internal/config"

	"gomodules.xyz/jsonpatch/v2"

	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	progressiveLabel = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
	gateName         = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
)

type Server struct {
	cfg *c.Config
	mux *http.ServeMux
}

func NewWebhookServer(cfg *c.Config) *Server {
	s := &Server{
		cfg: cfg,
		mux: http.NewServeMux(),
	}
	s.mux.HandleFunc("/daemonset/mutate", s.serveDaemonsetMutate)
	s.mux.HandleFunc("/pod/mutate", s.servePodMutate)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) serveDaemonsetMutate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read request", http.StatusBadRequest)
		return
	}

	var admissionReviewReq admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReviewReq); err != nil {
		http.Error(w, "could not unmarshal request", http.StatusBadRequest)
		return
	}

	admissionResponse := &admissionv1.AdmissionResponse{
		UID: admissionReviewReq.Request.UID,
	}
	operation := admissionReviewReq.Request.Operation
	s.handleDaemonset(admissionReviewReq, admissionResponse, w, operation)
}

func (s *Server) servePodMutate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read request", http.StatusBadRequest)
		return
	}

	var admissionReviewReq admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReviewReq); err != nil {
		http.Error(w, "could not unmarshal request", http.StatusBadRequest)
		return
	}

	admissionResponse := &admissionv1.AdmissionResponse{
		UID: admissionReviewReq.Request.UID,
	}
	s.handlePod(admissionReviewReq, admissionResponse, w)
}

func (s *Server) handleDaemonset(admissionReviewReq admissionv1.AdmissionReview, admissionResponse *admissionv1.AdmissionResponse, w http.ResponseWriter, operation admissionv1.Operation) {
	var daemonSet appsv1.DaemonSet
	if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &daemonSet); err != nil {
		admissionResponse.Allowed = false
		admissionResponse.Result = &metav1.Status{
			Message: "failed to unmarshal daemonset " + err.Error(),
		}
		writeResponse(w, admissionResponse)
		return
	}
	// For CREATE events, add the label to DaemonSet metadata and pod template.
	if operation == admissionv1.Create {
		// Patch DaemonSet metadata.labels. only when AddLabelOnCreate is true
		if s.cfg.AutoIncludeNewDaemonsets {
			if daemonSet.ObjectMeta.Labels == nil {
				daemonSet.ObjectMeta.Labels = map[string]string{}
			}
			daemonSet.ObjectMeta.Labels[progressiveLabel] = "true"
			s.cfg.Logger.Infof("Ensured %s label exists in daemonset %s's metadata\n", progressiveLabel, daemonSet.Name)
		}

		// Patch Daemonset pod template metadata labels if progressiveLabel is set
		if daemonSet.ObjectMeta.Labels != nil && daemonSet.ObjectMeta.Labels[progressiveLabel] == "true" {
			if daemonSet.Spec.Template.Labels == nil {
				daemonSet.Spec.Template.Labels = map[string]string{}
			}
			daemonSet.Spec.Template.Labels[progressiveLabel] = "true"
			s.cfg.Logger.Infof("Ensured %s label exists in daemonset %s's pod template\n", progressiveLabel, daemonSet.Name)
		}
	}

	// For UPDATE events, remove the label if the rollout is complete.
	if operation == admissionv1.Update {
		// Check the status for rollout completion.
		if daemonSet.Status.DesiredNumberScheduled > 0 &&
			daemonSet.Status.DesiredNumberScheduled == daemonSet.Status.CurrentNumberScheduled &&
			daemonSet.Status.UpdatedNumberScheduled == daemonSet.Status.NumberAvailable {

			if daemonSet.Spec.Template.Labels != nil && daemonSet.ObjectMeta.Labels[progressiveLabel] != "" {
				if daemonSet.Spec.Template.Labels[progressiveLabel] == "true" {
					delete(daemonSet.Spec.Template.Labels, progressiveLabel)
					s.cfg.Logger.Infof("Removed %s label from daemonset %s's pod template\n", progressiveLabel, daemonSet.Name)
				}
			}
		}
	}

	originalReq := admissionReviewReq.Request.Object.Raw
	patch(admissionResponse, daemonSet, w, originalReq)
}

func (s *Server) handlePod(admissionReviewReq admissionv1.AdmissionReview, admissionResponse *admissionv1.AdmissionResponse, w http.ResponseWriter) {
	var pod corev1.Pod
	if err := json.Unmarshal(admissionReviewReq.Request.Object.Raw, &pod); err != nil {
		admissionResponse.Allowed = false
		admissionResponse.Result = &metav1.Status{
			Message: "failed to unmarshal pod " + err.Error(),
		}
		writeResponse(w, admissionResponse)
		return
	}

	// Append the scheduling gate.
	pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{
		Name: gateName,
	})
	s.cfg.Logger.Infof("Added %s scheduling gate to pod\n", gateName)

	originalReq := admissionReviewReq.Request.Object.Raw
	patch(admissionResponse, pod, w, originalReq)
}

func patch[T corev1.Pod | appsv1.DaemonSet](admissionResponse *admissionv1.AdmissionResponse, object T, w http.ResponseWriter, originalReq []byte) {
	updated, err := json.Marshal(object)
	if err != nil {
		admissionResponse.Allowed = false
		admissionResponse.Result = &metav1.Status{
			Message: "failed to marshal updated resource " + err.Error(),
		}
		writeResponse(w, admissionResponse)
		return
	}
	patchOps, err := jsonpatch.CreatePatch(originalReq, updated)
	if err != nil {
		admissionResponse.Allowed = false
		admissionResponse.Result = &metav1.Status{
			Message: "failed to create patch for updated resource " + err.Error(),
		}
		writeResponse(w, admissionResponse)
		return
	}
	patchBytes, err := json.Marshal(patchOps)
	if err != nil {
		admissionResponse.Allowed = false
		admissionResponse.Result = &metav1.Status{
			Message: "failed to marshal patch for updated resource " + err.Error(),
		}
		writeResponse(w, admissionResponse)
		return
	}

	patchType := admissionv1.PatchTypeJSONPatch
	admissionResponse.Allowed = true
	admissionResponse.Patch = patchBytes
	admissionResponse.PatchType = &patchType
	writeResponse(w, admissionResponse)
}

func writeResponse(w http.ResponseWriter, response *admissionv1.AdmissionResponse) {
	admissionReviewResponse := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "admission.k8s.io/v1",
			Kind:       "AdmissionReview",
		},
		Response: response,
	}
	respBytes, err := json.Marshal(admissionReviewResponse)
	if err != nil {
		http.Error(w, "could not marshal response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}
