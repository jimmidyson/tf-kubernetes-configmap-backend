/*
 * Copyright 2019 Jimmi Dyson
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
 */

package http

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	minifyjson "github.com/tdewolff/minify/v2/json"
	authenticationapi "k8s.io/api/authentication/v1"
	authorizationapi "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authenticationv1 "k8s.io/client-go/kubernetes/typed/authentication/v1"
	authorizationv1 "k8s.io/client-go/kubernetes/typed/authorization/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	MethodLock   = "LOCK"
	MethodUnlock = "UNLOCK"

	annotationKeyPrefix        = "tf-kubernetes-configmap-backend.jimmidyson.github.com/"
	annotationKeyLockID        = annotationKeyPrefix + "lock-id"
	annotationKeyLockOperation = annotationKeyPrefix + "lock-operation"
	annotationKeyLockInfo      = annotationKeyPrefix + "lock-info"
	annotationKeyLockWho       = annotationKeyPrefix + "lock-who"
)

type handler struct {
	coreClient           corev1.CoreV1Interface
	authenticationClient authenticationv1.TokenReviewInterface
	authorizationClient  authorizationv1.SubjectAccessReviewInterface
	compressState        bool
	minifyState          bool
}

func NewHandler(
	coreClient corev1.CoreV1Interface,
	authenticationClient authenticationv1.TokenReviewInterface,
	authorizationClient authorizationv1.SubjectAccessReviewInterface,
	compressState bool,
	minifyState bool,
) http.Handler {
	return &handler{
		coreClient:           coreClient,
		authenticationClient: authenticationClient,
		authorizationClient:  authorizationClient,
		compressState:        compressState,
		minifyState:          minifyState,
	}
}

// lockInfo stores lock metadata.
//
// Copied and trimmed from https://github.com/hashicorp/terraform/blob/master/states/statemgr/locker.go#L110-L138
type lockInfo struct {
	// Unique ID for the lock.
	ID string
	// Terraform operation, provided by the caller.
	Operation string
	// Extra information to store with the lock, provided by the caller.
	Info string
	// user@hostname when available
	Who string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	_, token, ok := req.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Terraform "`)
		w.WriteHeader(401)
		return
	}

	tokenReviewResponse, err := h.authenticationClient.Create(&authenticationapi.TokenReview{
		Spec: authenticationapi.TokenReviewSpec{
			Token: token,
		},
	})
	if err != nil {
		log.Printf("failed to validate authentication token: %v", err)
		h.handleAPIError(err, w)
		return
	}
	if !tokenReviewResponse.Status.Authenticated {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	userInfo := tokenReviewResponse.Status.User

	log.Print(req.URL.Path)

	splitPath := strings.Split(req.URL.Path[1:], "/")
	if len(splitPath) != 2 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	namespace := splitPath[0]
	configMapName := splitPath[1]

	sarResponse, err := h.authorizationClient.Create(&authorizationapi.SubjectAccessReview{
		Spec: authorizationapi.SubjectAccessReviewSpec{
			User: userInfo.Username,
			UID:  userInfo.UID,
			ResourceAttributes: &authorizationapi.ResourceAttributes{
				Resource:  "configmaps",
				Namespace: namespace,
				Name:      configMapName,
				Verb:      "get",
			},
		},
	})
	if err != nil {
		log.Printf("failed to check authorization: %v", err)
		h.handleAPIError(err, w)
		return
	}

	if !sarResponse.Status.Allowed {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	apiVerb := "get"

	exists := true
	configMapClient := h.coreClient.ConfigMaps(namespace)
	configMap, err := configMapClient.Get(configMapName, metav1.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			log.Printf("failed to get configmap: %v", err)
			h.handleAPIError(err, w)
			return
		}
		exists = false
	}

	switch req.Method {
	case http.MethodGet:
		h.handleGET(configMap, w)
	case http.MethodPost:
		if exists {
			apiVerb = "update"
		} else {
			apiVerb = "create"
		}
		h.handlePOST(configMap, configMapClient, apiVerb, namespace, configMapName, userInfo, req, w)
	case http.MethodDelete:
		h.handleDELETE(configMap, configMapClient, namespace, configMapName, userInfo, req, w)
	case MethodLock:
		if exists {
			apiVerb = "update"
		} else {
			apiVerb = "create"
		}
		h.handleLOCK(configMap, configMapClient, apiVerb, namespace, configMapName, userInfo, req, w)
	case MethodUnlock:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		h.handleUNLOCK(configMap, configMapClient, namespace, configMapName, userInfo, req, w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}

}

func (h *handler) handleGET(configMap *v1.ConfigMap, w http.ResponseWriter) {
	if state, ok := configMap.BinaryData["tfstate"]; ok {
		var r io.Reader = bytes.NewReader(state)
		if h.compressState {
			var err error
			r, err = gzip.NewReader(r)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, "failed to read compressed Terraform state: %s", err)
				return
			}
		}
		if _, err := io.Copy(w, r); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to return Terraform state: %s", err)
			return
		}
		if rc, ok := r.(io.Closer); ok {
			if err := rc.Close(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, "failed to return Terraform state: %s", err)
				return
			}
		}
	}
}

func (h *handler) handlePOST(configMap *v1.ConfigMap, configMapClient corev1.ConfigMapInterface,
	apiVerb, namespace, configMapName string, userInfo authenticationapi.UserInfo,
	req *http.Request, w http.ResponseWriter) {
	err := h.checkAccess(apiVerb, namespace, configMapName, userInfo)
	if err != nil {
		log.Printf("failed to check access to update configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}

	// If the configmap is locked, then check the request comes from the locker.
	if !h.checkRequestIsFromLocker(configMap, w, req) {
		return
	}

	reqTFState, err := h.getTFStateForWriting(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to read request body: %s", err)
		return
	}

	if configMap.BinaryData == nil {
		configMap.BinaryData = make(map[string][]byte, 1)
	}
	configMap.BinaryData["tfstate"] = reqTFState

	switch apiVerb {
	case "update":
		configMap, err = configMapClient.Update(configMap)
	case "create":
		configMap.Name = configMapName
		configMap, err = configMapClient.Create(configMap)
	}

	if err != nil {
		log.Printf("failed to create/update configmap: %v", err)
		h.handleAPIError(err, w)
	}
}

func (h *handler) handleDELETE(configMap *v1.ConfigMap, configMapClient corev1.ConfigMapInterface,
	namespace, configMapName string, userInfo authenticationapi.UserInfo,
	req *http.Request, w http.ResponseWriter) {
	err := h.checkAccess("delete", namespace, configMapName, userInfo)
	if err != nil {
		log.Printf("failed to check access to delete configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}

	// If the configmap is locked, then check the request comes from the locker.
	if !h.checkRequestIsFromLocker(configMap, w, req) {
		return
	}

	if err = configMapClient.Delete(configMapName, &metav1.DeleteOptions{}); err != nil && errors.IsNotFound(err) {
		log.Printf("failed to delete configmap: %v", err)
		h.handleAPIError(err, w)
	}
}

func (h *handler) handleLOCK(configMap *v1.ConfigMap, configMapClient corev1.ConfigMapInterface,
	apiVerb, namespace, configMapName string, userInfo authenticationapi.UserInfo,
	req *http.Request, w http.ResponseWriter) {
	err := h.checkAccess(apiVerb, namespace, configMapName, userInfo)
	if err != nil {
		log.Printf("failed to check access to update configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}

	requestLockInfo := &lockInfo{}
	if err := json.NewDecoder(req.Body).Decode(requestLockInfo); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "failed to read request body: %s", err)
		return
	}

	if currentLockID, locked := configMap.Annotations[annotationKeyLockID]; locked &&
		currentLockID != requestLockInfo.ID {
		existingLockInfo := lockInfo{
			ID:        configMap.Annotations[annotationKeyLockID],
			Operation: configMap.Annotations[annotationKeyLockOperation],
			Info:      configMap.Annotations[annotationKeyLockInfo],
			Who:       configMap.Annotations[annotationKeyLockWho],
		}
		w.WriteHeader(http.StatusLocked)
		_ = json.NewEncoder(w).Encode(existingLockInfo)
		return
	}

	if configMap.Annotations == nil {
		configMap.Annotations = make(map[string]string, 4)
	}

	configMap.Annotations[annotationKeyLockID] = requestLockInfo.ID
	configMap.Annotations[annotationKeyLockOperation] = requestLockInfo.Operation
	configMap.Annotations[annotationKeyLockInfo] = requestLockInfo.Info
	configMap.Annotations[annotationKeyLockWho] = requestLockInfo.Who

	switch apiVerb {
	case "update":
		configMap, err = configMapClient.Update(configMap)
	case "create":
		configMap.Name = configMapName
		configMap, err = configMapClient.Create(configMap)
	}

	if err != nil {
		log.Printf("failed to lock configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}
}

func (h *handler) handleUNLOCK(configMap *v1.ConfigMap, configMapClient corev1.ConfigMapInterface,
	namespace, configMapName string, userInfo authenticationapi.UserInfo,
	req *http.Request, w http.ResponseWriter) {
	err := h.checkAccess("update", namespace, configMapName, userInfo)
	if err != nil {
		log.Printf("failed to check access to update configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}

	if req.ContentLength > 0 {
		requestLockInfo := &lockInfo{}
		if err := json.NewDecoder(req.Body).Decode(requestLockInfo); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "failed to read request body: %s", err)
			return
		}

		if currentLockID, locked := configMap.Annotations[annotationKeyLockID]; locked &&
			currentLockID != requestLockInfo.ID {
			existingLockInfo := lockInfo{
				ID:        configMap.Annotations[annotationKeyLockID],
				Operation: configMap.Annotations[annotationKeyLockOperation],
				Info:      configMap.Annotations[annotationKeyLockInfo],
				Who:       configMap.Annotations[annotationKeyLockWho],
			}
			w.WriteHeader(http.StatusLocked)
			_ = json.NewEncoder(w).Encode(existingLockInfo)
			return
		}
	}

	delete(configMap.Annotations, annotationKeyLockID)
	delete(configMap.Annotations, annotationKeyLockOperation)
	delete(configMap.Annotations, annotationKeyLockInfo)
	delete(configMap.Annotations, annotationKeyLockWho)

	configMap, err = configMapClient.Update(configMap)
	if err != nil {
		log.Printf("failed to unlock configmap: %v", err)
		h.handleAPIError(err, w)
		return
	}
}

func (h *handler) checkRequestIsFromLocker(configMap *v1.ConfigMap, w http.ResponseWriter, req *http.Request) bool {
	if configMap.Annotations[annotationKeyLockID] != req.URL.Query().Get("ID") {
		existingLockInfo := lockInfo{
			ID:        configMap.Annotations[annotationKeyLockID],
			Operation: configMap.Annotations[annotationKeyLockOperation],
			Info:      configMap.Annotations[annotationKeyLockInfo],
			Who:       configMap.Annotations[annotationKeyLockWho],
		}
		w.WriteHeader(http.StatusLocked)
		_ = json.NewEncoder(w).Encode(existingLockInfo)
		return false
	}
	return true
}

func (h *handler) handleAPIError(err error, w http.ResponseWriter) {
	if statusError, ok := err.(*errors.StatusError); ok {
		w.WriteHeader(int(statusError.Status().Code))
		w.Write([]byte(statusError.Error()))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (h *handler) checkAccess(apiVerb, namespace, configMapName string, userInfo authenticationapi.UserInfo) error {
	sarResponse, err := h.authorizationClient.Create(&authorizationapi.SubjectAccessReview{
		Spec: authorizationapi.SubjectAccessReviewSpec{
			User: userInfo.Username,
			UID:  userInfo.UID,
			ResourceAttributes: &authorizationapi.ResourceAttributes{
				Resource:  "configmaps",
				Namespace: namespace,
				Name:      configMapName,
				Verb:      apiVerb,
			},
		},
	})
	if err != nil {
		log.Printf("failed to check authorization: %v", err)
		return err
	}

	if !sarResponse.Status.Allowed {
		return errors.NewForbidden(v1.SchemeGroupVersion.WithResource("configmaps").GroupResource(), configMapName, nil)
	}

	return nil
}

func (h *handler) getTFStateForWriting(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	w := io.Writer(&buf)
	if h.compressState {
		gzw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		w = gzw
	}
	if h.minifyState {
		if err := minifyjson.Minify(nil, w, r, nil); err != nil {
			return nil, err
		}
	} else if _, err := io.Copy(w, r); err != nil {
		return nil, err
	}
	if wc, ok := w.(io.Closer); ok {
		if err := wc.Close(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
