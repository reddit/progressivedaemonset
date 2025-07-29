package config

import (
	"time"

	"github.com/spf13/pflag"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/reddit/progressivedaemonset/internal/metrics"
)

var (
	rolloutIntervalAnnotation = "infrared.reddit.com/daemonset-progressive-first-rollout-interval"
)

type Options struct {
	RolloutInterval time.Duration
}

func (c *Config) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&c.AutoIncludeNewDaemonsets, "auto-include-new-daemonsets", false, "Boolean value (default false) for daemonset mutation on CREATE to add the 'infrared.reddit.com/daemonset-progressive-first-rollout-enabled': 'true' labels, which opts pods into progressive rollouts")
	fs.DurationVar(&c.DaemonsetOptions.RolloutInterval, "rollout-interval", 5*time.Second, "Time interval (default 5 seconds) before a new pod can be scheduled. Once this interval elapses, the scheduling gate is removed from the pod")
}

type Config struct {
	DaemonsetOptions         *Options
	Logger                   *zap.SugaredLogger
	Clientset                *kubernetes.Clientset
	Metrics                  *metrics.Metrics
	AutoIncludeNewDaemonsets bool // New daemonsets need to match webhook's objectSelector to be included. Leaving this flag here to possibly be used in the future.
}

// CreateDsOptions creates a new Options struct based on the provided DaemonSet.
// If no annotations are found, it returns the default options that were initialized on start up.
func CreateDsOptions(ds *appsv1.DaemonSet, logger *zap.SugaredLogger, defaultOptions *Options) *Options {
	if ds == nil || ds.Annotations == nil {
		logger.Warn("Daemonset is nil or has no annotations")
		return defaultOptions
	}
	opts := &Options{}
	setRolloutInterval(ds, opts.RolloutInterval, &opts.RolloutInterval, logger, defaultOptions)
	return opts
}

// setRolloutInterval checks the DaemonSet annotations for a custom rollout interval
func setRolloutInterval(ds *appsv1.DaemonSet, currentDuration time.Duration, targetDuration *time.Duration, logger *zap.SugaredLogger, defaultOptions *Options) {
	if intervalStr, exists := ds.Annotations[rolloutIntervalAnnotation]; exists {
		if duration, err := time.ParseDuration(intervalStr); err == nil && duration > 0 {
			if currentDuration != duration {
				logger.Infof("Overriding rollout interval for DaemonSet %s/%s to %v", ds.Namespace, ds.Name, duration)
				*targetDuration = duration
			}
		} else if err != nil {
			logger.Warnf("Invalid rollout interval annotation for DaemonSet %s/%s: %s (error: %v). Using default rollout interval of %v", ds.Namespace, ds.Name, intervalStr, err, currentDuration)
		}
	} else {
		// If the annotation is not present, use the default value
		*targetDuration = defaultOptions.RolloutInterval
	}
}
