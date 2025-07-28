package controller_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"

	zaplog "go.uber.org/zap"

	c "github.com/reddit/progressivedaemonset/internal/config"
	"github.com/reddit/progressivedaemonset/internal/controller"
	"github.com/reddit/progressivedaemonset/internal/metrics"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	testEnv                *envtest.Environment
	testKubeConfig         *rest.Config // Store shared kubeconfig
	schedulingGate         = controller.SchedulingGateName
	defaultRolloutInterval = 100 * time.Millisecond // Default interval for the test cases
	progressiveLabel       = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
)

func TestMain(m *testing.M) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	sch := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(sch))

	assetsDir := os.Getenv("KUBEBUILDER_ASSETS")
	if assetsDir == "" {
		fmt.Println("KUBEBUILDER_ASSETS not set; skipping envtest-based tests")
		os.Exit(0)
	}

	testEnv = &envtest.Environment{}

	var err error
	testKubeConfig, err = testEnv.Start()
	if err != nil {
		fmt.Printf("Failed to start test environment: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		fmt.Printf("Failed to stop test environment: %v\n", err)
	}
	os.Exit(code)
}

// Helper function to set up each test with shared config
// Note: since environment is shared, namespaces should be unique for resources per test
func setupTest(t *testing.T, rolloutInterval time.Duration, namespace string) (*kubernetes.Clientset, *c.Config, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	client, err := kubernetes.NewForConfig(testKubeConfig)
	if err != nil {
		t.Fatalf("Failed to create client: %v\n", err)
	}

	log, _ := zaplog.NewProduction()
	logger := log.Sugar()

	config := &c.Config{
		DaemonsetOptions:         &c.Options{RolloutInterval: rolloutInterval},
		Logger:                   logger,
		Clientset:                client,
		AutoIncludeNewDaemonsets: true,
		Metrics:                  metrics.NewMetrics(logger, prometheus.NewRegistry()),
	}
	_, err = client.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		t.Fatalf("failed to create namespace: %v", err)
	}

	return client, config, ctx, cancel
}

func TestRemoveSchedulingGateIfExists(t *testing.T) {
	namespace := "remove-scheduling-gate-test"
	ds1 := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "ds"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "ds", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}

	client, config, ctx, cancel := setupTest(t, defaultRolloutInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS1, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ds: %v", err)
	}

	pod := createTestPod("pod-ds", createdDS1.Name, createdDS1.UID, namespace, true)

	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod: %v", err)
	}

	assert.Eventually(t, func() bool {
		updatedPod1, err1 := client.CoreV1().Pods(namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
		if err1 != nil {
			return false
		}
		return len(updatedPod1.Spec.SchedulingGates) == 0
	}, 30*time.Second, 1*time.Second, "expected both pods to have their scheduling gates removed")

}

func TestConcurrentDaemonSets(t *testing.T) {
	namespace := "concurrent-ds-test"
	ds1 := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds1",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "ds1"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "ds1", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}
	ds2 := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds2",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "ds2"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "ds2", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}

	client, config, ctx, cancel := setupTest(t, defaultRolloutInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS1, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ds1: %v", err)
	}
	createdDS2, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds2, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ds2: %v", err)
	}

	pod1 := createTestPod("pod-ds1", createdDS1.Name, createdDS1.UID, namespace, true)
	pod2 := createTestPod("pod-ds2", createdDS2.Name, createdDS2.UID, namespace, true)

	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod-ds1: %v", err)
	}
	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod2, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod-ds2: %v", err)
	}

	assert.Eventually(t, func() bool {
		updatedPod1, err1 := client.CoreV1().Pods(namespace).Get(context.Background(), pod1.Name, metav1.GetOptions{})
		updatedPod2, err2 := client.CoreV1().Pods(namespace).Get(context.Background(), pod2.Name, metav1.GetOptions{})
		if err1 != nil || err2 != nil {
			return false
		}
		return len(updatedPod1.Spec.SchedulingGates) == 0 && len(updatedPod2.Spec.SchedulingGates) == 0
	}, 30*time.Second, 1*time.Second, "expected both pods to have their scheduling gates removed")
}

func TestWithNoSchedulingGate(t *testing.T) {
	namespace := "no-scheduling-gate-test"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "ds"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "ds", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}
	client, config, ctx, cancel := setupTest(t, defaultRolloutInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ds: %v", err)
	}
	trueValue := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default-pod",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       createdDS.Name,
					UID:        createdDS.UID,
					Controller: &trueValue,
				},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "busybox",
				Image: "busybox",
			}},
		},
	}

	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod: %v", err)
	}

	assert.Eventually(t, func() bool {
		updatedPod1, err1 := client.CoreV1().Pods(namespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
		if err1 != nil {
			return false
		}
		return len(updatedPod1.Spec.SchedulingGates) == 0
	}, 30*time.Second, 1*time.Second, "expected pod to be unchanged")
}

func TestRateLimitedGateRemoval(t *testing.T) {
	namespace := "rate-limited-removal-test"
	ds1 := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "ds"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "ds", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}

	client, config, ctx, cancel := setupTest(t, defaultRolloutInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS1, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create ds: %v", err)
	}

	pod1 := createTestPod("pod-ds1", createdDS1.Name, createdDS1.UID, namespace, true)
	pod2 := createTestPod("pod-ds2", createdDS1.Name, createdDS1.UID, namespace, true)

	var t1, t2 time.Time

	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod-1: %v", err)
	}
	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod2, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod-2: %v", err)
	}

	assert.Eventually(t, func() bool {
		now := time.Now()
		p1, err1 := client.CoreV1().Pods(namespace).Get(context.Background(), pod1.Name, metav1.GetOptions{})
		p2, err2 := client.CoreV1().Pods(namespace).Get(context.Background(), pod2.Name, metav1.GetOptions{})
		if err1 != nil || err2 != nil {
			return false
		}

		if len(p1.Spec.SchedulingGates) == 0 && t1.IsZero() {
			t1 = now
		}

		if len(p2.Spec.SchedulingGates) == 0 && t2.IsZero() {
			t2 = now
		}
		return !t1.IsZero() && !t2.IsZero()
	}, 35*time.Second, 1*time.Second, "expected both pods to have their scheduling gates removed")

	// ensure that the time difference between the two removals is at least defaultRolloutInterval long
	var diff time.Duration
	if t1.Before(t2) {
		diff = t2.Sub(t1)
	} else {
		diff = t1.Sub(t2)
	}

	diff = diff.Round(time.Second)
	if diff < defaultRolloutInterval {
		t.Fatalf("expected at least %s delay between removals, got %s", defaultRolloutInterval, diff)
	} else {
		fmt.Printf("rate limiting test passed; time difference between gate removals: %s\n", diff)
	}
}

func TestDaemonSetUpdateDuringRollout(t *testing.T) {
	namespace := "update-ds-test"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "update-ds",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "update-ds"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "update-ds", progressiveLabel: "true"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}

	client, config, ctx, cancel := setupTest(t, defaultRolloutInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS, err := client.AppsV1().DaemonSets(namespace).Create(
		context.Background(), ds, metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("failed to create daemonset: %v", err)
	}

	numPods := 4
	pods := make([]*corev1.Pod, numPods)
	for i := 0; i < numPods; i++ {
		pods[i] = createTestPod(
			fmt.Sprintf("update-test-pod-v1-%d", i),
			createdDS.Name,
			createdDS.UID,
			namespace,
			true,
		)
		_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pods[i], metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create pod %d: %v", i, err)
		}
		// Validate that pods have the scheduling gate injected
		assert.Equal(t, 1, len(pods[i].Spec.SchedulingGates), "pod should have a scheduling gate")
		assert.Equal(t, schedulingGate, pods[i].Spec.SchedulingGates[0].Name, "pod should have the correct scheduling gate")
	}

	// Wait for first pod to have its gate removed, then update the DaemonSet
	assert.Eventually(t, func() bool {
		podList, err := client.CoreV1().Pods(namespace).List(
			context.Background(),
			metav1.ListOptions{
				LabelSelector: "infrared.reddit.com/daemonset-progressive-first-rollout-enabled=true",
			},
		)
		if err != nil {
			t.Fatalf("Failed to list pods: %v", err)
			return false
		}

		// Check if any pod has had its scheduling gate removed
		anyPodUpdated := false
		podsWithGates := []*corev1.Pod{}

		for i := range podList.Items {
			pod := &podList.Items[i]
			if len(pod.Spec.SchedulingGates) == 0 {
				anyPodUpdated = true
			} else {
				podsWithGates = append(podsWithGates, pod)
			}
		}
		if !anyPodUpdated {
			t.Logf("No pod has had its scheduling gate removed")
			return false
		}

		// Update the pod template spec to trigger update
		createdDS.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
		// Also add a version label to the DaemonSet for tracking
		createdDS.Labels["version"] = "v2"
		_, err = client.AppsV1().DaemonSets(namespace).Update(
			context.Background(), createdDS, metav1.UpdateOptions{},
		)
		if err != nil {
			t.Logf("Failed to update DaemonSet: %v", err)
			return false
		}

		// Delete pods that still have the scheduling gate (unscheduled), this emulates behavior of the daemonset controller
		for _, pod := range podsWithGates {
			err = client.CoreV1().Pods(namespace).Delete(
				context.Background(),
				pod.Name,
				metav1.DeleteOptions{},
			)
			if err != nil {
				t.Fatalf("Failed to delete pod %s: %v", pod.Name, err)
			}
		}

		// Create new pods with updated pod spec
		pods = make([]*corev1.Pod, numPods)
		for i := 1; i < numPods; i++ {
			pods[i] = createTestPod(
				fmt.Sprintf("update-test-pod-v2-%d", i),
				createdDS.Name,
				createdDS.UID,
				namespace,
				true,
			)
			_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pods[i], metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to create pod %d: %v", i, err)
			}
		}
		return true
	}, 15*time.Second, 1*time.Second, "No pod had its gate removed in time")

	// Verify that all remaining pods eventually have their gates removed
	assert.Eventually(t, func() bool {
		allUpdated := true
		for i := 0; i < numPods; i++ {
			pod, err := client.CoreV1().Pods(namespace).Get(
				context.Background(),
				fmt.Sprintf("update-test-pod-v2-%d", i),
				metav1.GetOptions{},
			)
			// Skip only if the pod wasn't found, fail on other errors
			if err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				t.Fatalf("unexpected error getting pod: %v", err)
			}

			if len(pod.Spec.SchedulingGates) > 0 {
				allUpdated = false
				break
			}
		}
		return allUpdated
		// with 4 pods, rollout should not take more than 20 seconds.
	}, 20*time.Second, 1*time.Second, "Not all pods had their scheduling gates removed in expected timeframe")
}

func TestUpdateRolloutIntervalViaAnnotation(t *testing.T) {
	namespace := "update-rollout-interval-test"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "interval-ds",
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": "interval-ds"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name": "interval-ds",
						progressiveLabel:         "true",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "busybox",
						Image: "busybox",
					}},
				},
			},
		},
	}
	initialInterval := 100 * time.Millisecond
	updatedInterval := 250 * time.Millisecond // Much longer interval for clear difference

	client, config, ctx, cancel := setupTest(t, initialInterval, namespace)
	defer cancel()

	go func() {
		controller.StartController(ctx, config)
	}()

	createdDS, err := client.AppsV1().DaemonSets(namespace).Create(context.Background(), ds, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create daemonset: %v", err)
	}

	// Create all four pods upfront
	pod1 := createTestPod("interval-pod-1", createdDS.Name, createdDS.UID, namespace, true)
	pod2 := createTestPod("interval-pod-2", createdDS.Name, createdDS.UID, namespace, true)
	pod3 := createTestPod("interval-pod-3", createdDS.Name, createdDS.UID, namespace, true)
	pod4 := createTestPod("interval-pod-4", createdDS.Name, createdDS.UID, namespace, true)

	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod 1: %v", err)
	}
	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod2, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod 2: %v", err)
	}
	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod3, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod 3: %v", err)
	}
	_, err = client.CoreV1().Pods(namespace).Create(context.Background(), pod4, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create pod 4: %v", err)
	}

	var pod1GateRemoved, pod2GateRemoved time.Time

	// Wait for first two pods to have gates removed and capture the timing
	assert.Eventually(t, func() bool {
		p1, err := client.CoreV1().Pods(namespace).Get(context.Background(), pod1.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		p2, err := client.CoreV1().Pods(namespace).Get(context.Background(), pod2.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(p1.Spec.SchedulingGates) == 0 && pod1GateRemoved.IsZero() {
			pod1GateRemoved = time.Now()
			t.Logf("Pod 1 gate removed at: %v", pod1GateRemoved.Format(time.RFC3339Nano))
		}

		if len(p2.Spec.SchedulingGates) == 0 && pod2GateRemoved.IsZero() {
			pod2GateRemoved = time.Now()
			t.Logf("Pod 2 gate removed at: %v", pod2GateRemoved.Format(time.RFC3339Nano))
		}

		return !pod1GateRemoved.IsZero() && !pod2GateRemoved.IsZero()
	}, 15*time.Second, 100*time.Millisecond, "first two pods should have scheduling gates removed")

	defaultTimeDiff := pod2GateRemoved.Sub(pod1GateRemoved)
	t.Logf("Default interval time between pod gate removals: %v", defaultTimeDiff.Round(time.Millisecond))

	if defaultTimeDiff < initialInterval/2 || defaultTimeDiff > initialInterval*3/2 {
		t.Errorf("Expected time difference around %v, got %v", initialInterval, defaultTimeDiff)
	}

	// Now update the DaemonSet with a longer rollout interval annotation
	createdDS.Annotations = map[string]string{
		"infrared.reddit.com/daemonset-progressive-first-rollout-interval": updatedInterval.String(),
	}
	_, err = client.AppsV1().DaemonSets(namespace).Update(
		context.Background(), createdDS, metav1.UpdateOptions{},
	)
	if err != nil {
		t.Fatalf("failed to update daemonset: %v", err)
	}

	var pod3GateRemoved, pod4GateRemoved time.Time

	// Wait for remaining pods to have gates removed and capture the timing
	assert.Eventually(t, func() bool {
		p3, err := client.CoreV1().Pods(namespace).Get(context.Background(), pod3.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		p4, err := client.CoreV1().Pods(namespace).Get(context.Background(), pod4.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(p3.Spec.SchedulingGates) == 0 && pod3GateRemoved.IsZero() {
			pod3GateRemoved = time.Now()
			t.Logf("Pod 3 gate removed at: %v", pod3GateRemoved.Format(time.RFC3339Nano))
		}

		if len(p4.Spec.SchedulingGates) == 0 && pod4GateRemoved.IsZero() {
			pod4GateRemoved = time.Now()
			t.Logf("Pod 4 gate removed at: %v", pod4GateRemoved.Format(time.RFC3339Nano))
		}

		return !pod3GateRemoved.IsZero() && !pod4GateRemoved.IsZero()
	}, 25*time.Second, 100*time.Millisecond, "remaining pods should have scheduling gates removed")

	updatedTimeDiff := pod4GateRemoved.Sub(pod3GateRemoved)
	t.Logf("Updated interval time between pod gate removals: %v", updatedTimeDiff.Round(time.Millisecond))

	// Verify time difference approximately matches the updated interval
	if updatedTimeDiff < updatedInterval/2 || updatedTimeDiff > updatedInterval*3/2 {
		t.Errorf("Expected time difference around %v, got %v", updatedInterval, updatedTimeDiff)
	}

	assert.Greater(t, updatedTimeDiff.Seconds(), defaultTimeDiff.Seconds()*1.3,
		"Updated interval should be significantly longer than the default interval")
}

// Helper to create a pod for a given daemonset.
func createTestPod(podName, dsName string, dsUID types.UID, namespace string, trueVal bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				progressiveLabel: "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       dsName,
					UID:        dsUID,
					Controller: &trueVal,
				},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "busybox",
				Image: "busybox",
			}},
			SchedulingGates: []corev1.PodSchedulingGate{
				{
					Name: schedulingGate,
				},
			},
		},
	}
}
