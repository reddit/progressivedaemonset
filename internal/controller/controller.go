package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"go.uber.org/zap"

	c "github.com/reddit/progressivedaemonset/internal/config"
	"github.com/reddit/progressivedaemonset/internal/metrics"
	"github.com/reddit/progressivedaemonset/internal/webhook"

	_ "k8s.io/client-go/plugin/pkg/client/auth" // enable cloud provider auth
)

const (
	podQueueSize       = 3000
	SchedulingGateName = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
	progressiveLabel   = "infrared.reddit.com/daemonset-progressive-first-rollout-enabled"
)

type synchronizedMap struct {
	dsMap map[string]chan string
	mu    sync.Mutex
}

func (m *synchronizedMap) Load(key string) (chan string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value, found := m.dsMap[key]; found {
		return value, found
	}
	podChannel := make(chan string, podQueueSize)
	m.dsMap[key] = podChannel
	return podChannel, false
}

func (m *synchronizedMap) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if podChannel, found := m.dsMap[key]; found {
		fmt.Printf("deleting pod channel for daemonset key: %s\n", key)
		close(podChannel)
		delete(m.dsMap, key)
	}
}

func (m *synchronizedMap) Write(dsKey string, podKey string) (chan string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if podChannel, found := m.dsMap[dsKey]; found {
		podChannel <- podKey
		return podChannel, found
	}
	podChannel := make(chan string, podQueueSize)
	m.dsMap[dsKey] = podChannel
	podChannel <- podKey
	return podChannel, false
}

func (m *synchronizedMap) Upsert(dsKey string) chan string {
	m.mu.Lock()
	defer m.mu.Unlock()
	podChannel := make(chan string, podQueueSize)
	oldChannel, found := m.dsMap[dsKey]
	if found {
		for len(oldChannel) > 0 {
			podChannel <- <-oldChannel
		}
		close(oldChannel)
	}
	m.dsMap[dsKey] = podChannel
	return podChannel
}

var (
	cfg = &c.Config{
		DaemonsetOptions: &c.Options{},
	}
	rootCmd = &cobra.Command{
		Use:   "manager",
		Short: "Start the progressive rollout controller",
		RunE:  run,
	}
)

func init() {
	cfg.AddFlags(rootCmd.Flags())
}

func run(cmd *cobra.Command, args []string) error {
	log, _ := zap.NewProduction()
	logger := log.Sugar()
	defer logger.Sync()

	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("building in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	cfg.Clientset = clientset
	cfg.Logger = logger
	registry := prometheus.NewRegistry()
	cfg.Metrics = metrics.NewMetrics(logger, registry)
	cfg.Metrics.StartServer("8080", registry)
	go func() {
		server := webhook.NewWebhookServer(cfg)
		logger.Infof("Starting webhook on :9443 (add-label-on-create=%t)", cfg.AutoIncludeNewDaemonsets)
		if err := http.ListenAndServeTLS(":9443", "/tls/tls.crt", "/tls/tls.key", server); err != nil {
			logger.Fatalf("Failed to start server: %v", err)
		}
	}()
	StartController(context.Background(), cfg)
	return nil
}

func StartController(ctx context.Context, cfg *c.Config) {
	cfg.Logger.Info("Starting progressive scheduling gate remover controller")

	var dsMap = synchronizedMap{
		dsMap: make(map[string]chan string),
	}

	factory := informers.NewSharedInformerFactory(cfg.Clientset, 10*time.Minute)

	podInformer := factory.Core().V1().Pods().Informer()
	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			if _, exists := pod.Labels[progressiveLabel]; exists {
				if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "DaemonSet" {
					cfg.Logger.Errorf("Pod %s/%s is not a daemonset", pod.Namespace, pod.Name)
				}
				owner := pod.OwnerReferences[0]
				if owner.Controller != nil && *owner.Controller {
					// Generate the dskey in the same format.
					dsKey := fmt.Sprintf("%s/%s", pod.Namespace, owner.Name)
					podKey, err := cache.MetaNamespaceKeyFunc(pod)
					if err != nil {
						cfg.Logger.Errorf("Error creating key for pod %s\n", pod.Name)
					} else {
						podChannel, existed := dsMap.Write(dsKey, podKey)
						cfg.Logger.Debugf("Pod channel length %v, for Daemonset: %s/%s", len(podChannel), pod.Namespace, owner.Name)
						if !existed {
							go process(ctx, podInformer, podChannel, owner.Name, *cfg.DaemonsetOptions, cfg.Logger, cfg.Clientset, cfg.Metrics)
							cfg.Logger.Infof("DaemonSet added with key %s\n", dsKey)
						}
					}
				}
			}
		},
	})

	if err != nil {
		cfg.Logger.Fatal("Error adding pod to informer")
	}

	daemonsetInformer := factory.Apps().V1().DaemonSets().Informer()
	_, err = daemonsetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			daemonset := obj.(*appsv1.DaemonSet)
			if _, exists := daemonset.Labels[progressiveLabel]; exists {
				dsKey, err := cache.MetaNamespaceKeyFunc(daemonset)
				if err != nil {
					cfg.Logger.Errorf("Error building key for daemonset: %s\n", daemonset.Name)
				}
				if podChannel, existed := dsMap.Load(dsKey); !existed && err == nil {
					dsOptions := c.CreateDsOptions(daemonset, cfg.Logger, cfg.DaemonsetOptions)
					go process(ctx, podInformer, podChannel, daemonset.Name, *dsOptions, cfg.Logger, cfg.Clientset, cfg.Metrics)
					cfg.Logger.Infof("DaemonSet added with key %s\n", dsKey)
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldDaemonset := oldObj.(*appsv1.DaemonSet)
			newDaemonset := newObj.(*appsv1.DaemonSet)
			// Only process updates for DaemonSets with the progressive label
			if _, exists := newDaemonset.Labels[progressiveLabel]; exists {
				// Check if any annotations have changed
				annotationsChanged := false
				if len(oldDaemonset.Annotations) != len(newDaemonset.Annotations) {
					annotationsChanged = true
				} else {
					for key, newValue := range newDaemonset.Annotations {
						if oldValue, exists := oldDaemonset.Annotations[key]; !exists || oldValue != newValue {
							annotationsChanged = true
							break
						}
					}
				}
				if annotationsChanged {
					dsKey, _ := cache.MetaNamespaceKeyFunc(newDaemonset)
					newChannel := dsMap.Upsert(dsKey)
					dsOptions := c.CreateDsOptions(newDaemonset, cfg.Logger, cfg.DaemonsetOptions)
					go process(ctx, podInformer, newChannel, newDaemonset.Name, *dsOptions, cfg.Logger, cfg.Clientset, cfg.Metrics)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			daemonset := obj.(*appsv1.DaemonSet)
			if _, exists := daemonset.Labels[progressiveLabel]; exists {
				dsKey, err := cache.MetaNamespaceKeyFunc(daemonset)
				if err != nil {
					cfg.Logger.Errorf("Error building key for daemonset: %v", err)
				} else {
					dsMap.Delete(dsKey)
				}
			}
		},
	})
	if err != nil {
		cfg.Logger.Fatalf("Error adding daemonset to informer: %v", err)
	}

	// graceful shutdown from SIGINT
	stopCh := make(chan struct{})
	defer close(stopCh)
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(stopCh, podInformer.HasSynced, daemonsetInformer.HasSynced) {
		cfg.Logger.Fatal("Timed out waiting for caches to sync")
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	for {
		<-time.After(10 * time.Second)
		select {
		case <-done:
			cfg.Logger.Info("Received sigint... shutting down")
			return
		case <-ctx.Done():
			cfg.Logger.Info("Context done... shutting down")
			return
		default:
			dsMap.mu.Lock()
			for dsKey, podChannel := range dsMap.dsMap {
				cfg.Logger.Debugf("Pod channel length %v, Dskey: %s", len(podChannel), dsKey)
				if len(podChannel) == 0 {
					delete(dsMap.dsMap, dsKey)
					close(podChannel)
				}
			}
			dsMap.mu.Unlock()
		}
	}
}

// process is used in goroutines to progress a given rollout. all arguments must either be threadsafe or copies of memory that this function can own and mutate on its own.
// threadsafe: podInformer, logger, clientset, dsMetrics
// owned: daemonsetName, options
// subscriber: podChan
func process(ctx context.Context, podInformer cache.SharedIndexInformer, podChan chan string, daemonsetName string, options c.Options, logger *zap.SugaredLogger, clientset *kubernetes.Clientset, dsMetrics *metrics.Metrics) {
	time.Sleep(1 * time.Second) // wait for channel to fill before processing for more accurate testing and metrics
	processed := false
	var namespace string
	var dsKey string
	dsMetrics.ActiveRollouts.Inc()
	defer dsMetrics.ActiveRollouts.Dec()
	for {
		if processed {
			<-time.After(options.RolloutInterval)
		}
		logger.Debugf("Processing pods for daemonset: %s\n", daemonsetName)
		select {
		case podKey, more := <-podChan:
			if !more {
				// Channel closed, verify if this is actually a completion
				verifyRolloutCompletion(ctx, namespace, daemonsetName, dsKey, logger, clientset, dsMetrics)
				return
			}
			logger.Debugf("Pod channel length %v, for Daemonset: %s\n", len(podChan), daemonsetName)
			processed = processPodByKey(podInformer, podKey, logger, clientset)
			if dsKey == "" {
				namespace, _, _ = cache.SplitMetaNamespaceKey(podKey)
				dsKey = fmt.Sprintf("%s/%s", namespace, daemonsetName)
			}
			if processed {
				dsMetrics.UpdateDaemonSetMetrics(dsKey, len(podChan), options.RolloutInterval)
			}
		default:
		}
	}
}

func verifyRolloutCompletion(ctx context.Context, namespace string, daemonsetName string, dsKey string, logger *zap.SugaredLogger, clientset *kubernetes.Clientset, dsMetrics *metrics.Metrics) {
	if namespace == "" {
		logger.Warnf("Cannot verify completion for daemonset %s: namespace unknown", daemonsetName)
		return
	}
	ds, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, daemonsetName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		logger.Warnf("Error retrieving DaemonSet %s/%s to verify completion: %v", namespace, daemonsetName, err)
		return
	}
	// Check if all pods are rolled out
	if rolloutComplete(ds) {
		logger.Infof("Rollout completed for daemonset: %s", daemonsetName)
		dsMetrics.RemoveDaemonSetMetrics(dsKey)
	} else {
		logger.Infof("Channel closed but rollout not complete for daemonset %s - likely updated", daemonsetName)
	}
}

func rolloutComplete(ds *appsv1.DaemonSet) bool {
	return ds.Status.DesiredNumberScheduled > 0 &&
		ds.Status.DesiredNumberScheduled == ds.Status.CurrentNumberScheduled &&
		ds.Status.UpdatedNumberScheduled == ds.Status.NumberAvailable
}

// processPodByKey retrieves the pod from the informer's cache and processes it.
func processPodByKey(informer cache.SharedIndexInformer, key string, logger *zap.SugaredLogger, clientset *kubernetes.Clientset) bool {
	obj, exists, err := informer.GetIndexer().GetByKey(key)
	if err != nil {
		logger.Errorf("Error retrieving object with key %s: %v\n", key, err)
		return false
	}
	if !exists {
		// Scheduling gated pods are deleted from the informers cache when a daemonset is updated with a new spec
		logger.Debugf("Pod with key %s no longer exists\n", key)
		return false
	}

	pod := obj.(*corev1.Pod)
	if err = removeSchedulingGateIfExists(clientset, pod, SchedulingGateName); err != nil {
		logger.Errorf("Error removing scheduling gate for pod %s/%s: %v\n", pod.Namespace, pod.Name, err)
		return false // TODO: consider retrying
	} else {
		logger.Infof("%s Removed scheduling gate from pod %s/%s\n", time.Now().Format("15:04:05"), pod.Namespace, pod.Name)
	}
	return true
}

func removeSchedulingGateIfExists(clientset *kubernetes.Clientset, pod *corev1.Pod, schedulingGateName string) error {
	newGates := []corev1.PodSchedulingGate{}
	found := false
	// TODO: consider using a resource lock
	for _, gate := range pod.Spec.SchedulingGates {
		if gate.Name == schedulingGateName {
			found = true
			continue
		}
		newGates = append(newGates, gate)
	}
	if !found {
		return fmt.Errorf("scheduling gate %s not found", schedulingGateName)
	}
	// Create a merge patch payload that sets the schedulingGates field to the new slice.
	patchPayload, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{
			"schedulingGates": newGates,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %v", err)
	}

	// Apply the patch.
	_, err = clientset.CoreV1().Pods(pod.Namespace).Patch(
		context.Background(),
		pod.Name,
		types.MergePatchType,
		patchPayload,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch pod: %v", err)
	}
	return nil
}

func Execute() error {
	return rootCmd.Execute()
}
