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
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

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
)

type handler struct {
	coreClient           corev1.CoreV1Interface
	authenticationClient authenticationv1.TokenReviewInterface
	authorizationClient  authorizationv1.SubjectAccessReviewInterface
	compressState        bool
}

func NewHandler(
	coreClient corev1.CoreV1Interface,
	authenticationClient authenticationv1.TokenReviewInterface,
	authorizationClient authorizationv1.SubjectAccessReviewInterface,
	compressState bool,
) http.Handler {
	return &handler{
		coreClient:           coreClient,
		authenticationClient: authenticationClient,
		authorizationClient:  authorizationClient,
		compressState:        compressState,
	}
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

		if statusError, ok := err.(*errors.StatusError); ok {
			w.WriteHeader(int(statusError.Status().Code))
			w.Write([]byte(statusError.Error()))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
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

	log.Print(splitPath)

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

		if statusError, ok := err.(*errors.StatusError); ok {
			w.WriteHeader(int(statusError.Status().Code))
			w.Write([]byte(statusError.Error()))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
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
			if statusError, ok := err.(*errors.StatusError); ok {
				w.WriteHeader(int(statusError.Status().Code))
				w.Write([]byte(statusError.Error()))
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
		exists = false
	}

	switch req.Method {
	case http.MethodGet:
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
		return
	case http.MethodPost:
		if exists {
			apiVerb = "update"
		} else {
			apiVerb = "create"
		}
		err = h.checkAccess(apiVerb, namespace, configMapName, userInfo)
		if err != nil {
			if statusError, ok := err.(*errors.StatusError); ok {
				w.WriteHeader(int(statusError.Status().Code))
				w.Write([]byte(statusError.Error()))
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
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
			if statusError, ok := err.(*errors.StatusError); ok {
				w.WriteHeader(int(statusError.Status().Code))
				w.Write([]byte(statusError.Error()))
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
	case http.MethodDelete:
		apiVerb = "delete"
	case MethodLock:
		w.WriteHeader(http.StatusNotImplemented)
	case MethodUnlock:
		w.WriteHeader(http.StatusNotImplemented)
	default:
		w.WriteHeader(http.StatusNotFound)
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
	if _, err := io.Copy(w, r); err != nil {
		return nil, err
	}
	if wc, ok := w.(io.Closer); ok {
		if err := wc.Close(); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
