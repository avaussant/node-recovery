/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */
package controller

import (
	"math/rand"
	"runtime/debug"
	"time"

	"github.com/golang/glog"

	"k8s.io/client-go/tools/cache"
)

func HandlePanic() {
	if r := recover(); r != nil {
		glog.Error("stacktrace", debug.Stack(), "msg", r)
	}
}

// WaitForCacheSync is a wrapper around cache.WaitForCacheSync that generates log messages
// indicating that the controller identified by controllerName is waiting for syncs, followed by
// either a successful or failed sync.
func WaitForCacheSync(controllerName string, stopCh <-chan struct{}, cacheSyncs ...cache.InformerSynced) bool {
	glog.Infof("waiting for caches to sync for %s controller", controllerName)

	if !cache.WaitForCacheSync(stopCh, cacheSyncs...) {
		glog.Errorf("unable to sync caches for %s controller", controllerName)
		return false
	}

	glog.Infof("caches are synced for %s controller", controllerName)
	return true
}

// ResyncPeriod returns resync period for informers
func ResyncPeriod(minResyncPeriod time.Duration) time.Duration {
	factor := rand.Float64() + 1
	return time.Duration(float64(minResyncPeriod.Nanoseconds()) * factor)
}

// DefaultResyncPeriod returns default resync period
func DefaultResyncPeriod() time.Duration {
	return ResyncPeriod(12 * time.Hour)
}
