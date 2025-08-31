// Copyright 2025 The Kubernetes Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package k8cache provides caching utilities for Kubernetes API responses.
// It includes middleware for intercepting cluster API requests, generating
// unique cache keys, storing and retrieving responses, and invalidating
// entries when resources change. The package aims to reduce redundant
// API calls, improve performance, and handle authorization gracefully
// while maintaining consistency across multiple Kubernetes contexts.
package k8cache

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/kubernetes-sigs/headlamp/backend/pkg/kubeconfig"
	"github.com/kubernetes-sigs/headlamp/backend/pkg/logger"
	"k8s.io/client-go/kubernetes"
)

type CachedClientSet struct {
	clientset *kubernetes.Clientset
	lastUsed  time.Time
}

var (
	clientsetCache = make(map[string]*CachedClientSet)
	mu             sync.Mutex
)

// GetClientSet return *kubernetes.ClientSet and error which further used for creating
// SSAR requests to k8s server to authorize user. GetClientSet uses kubeconfig.Context and
// authentication bearer token  which will help to create clientSet based on the user's
// identity.
func GetClientSet(k *kubeconfig.Context, token string) (*kubernetes.Clientset, error) {
	contextKey := strings.Split(k.ClusterID, "+")
	if len(contextKey) < 2 {
		return nil, fmt.Errorf("unexpected ClusterID format in getClientSet: %q", k.ClusterID)
	}

	cacheKey := fmt.Sprintf("%s-%s", contextKey[1], token)

	mu.Lock()
	defer mu.Unlock()

	if cs, found := clientsetCache[cacheKey]; found {
		now := time.Now()

		if now.Sub(cs.lastUsed) > 10*time.Minute { // If the clientset was expired then delete
			// the existing clientset resulting only fresh clientset.
			delete(clientsetCache, cacheKey)
			logger.Log(logger.LevelInfo, nil, nil, "clientset "+cacheKey+" was deleted")
		} else {
			return cs.clientset, nil // If the clientset is not expired then return directly.
		}
	}

	cs, err := k.ClientSetWithToken(token)
	if err != nil {
		return nil, fmt.Errorf("error while creating clientset for key %s: %w", cacheKey, err)
	}

	clientsetCache[cacheKey] = &CachedClientSet{
		clientset: cs,
		lastUsed:  time.Now(),
	}

	return cs, nil
}

// GetKindAndVerb extracts the Kubernetes resource kind and intended verb (e.g., get, watch)
// from the incoming HTTP request.
func GetKindAndVerb(r *http.Request) (string, string) {
	apiPath, ok := mux.Vars(r)["api"]
	if !ok || apiPath == "" {
		return "", "unknown"
	}

	parts := strings.Split(apiPath, "/")
	last := parts[len(parts)-1]

	var kubeVerb string

	isWatch, _ := strconv.ParseBool(r.URL.Query().Get("watch"))

	switch r.Method {
	case "GET":
		if isWatch {
			kubeVerb = "watch"
		} else {
			kubeVerb = "get"
		}
	default:
		kubeVerb = "unknown"
	}

	return last, kubeVerb
}
