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

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"time"

	flag "github.com/spf13/pflag"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/options"

	tfhttp "github.com/jimmidyson/tf-kubernetes-configmap-backend/pkg/http"
	"github.com/jimmidyson/tf-kubernetes-configmap-backend/pkg/kubernetes"
	"github.com/jimmidyson/tf-kubernetes-configmap-backend/pkg/version"
)

var (
	kubeconfig                      string
	delegatingAuthenticationOptions = options.NewDelegatingAuthenticationOptions()
	delegatingAuthorizationOptions  = options.NewDelegatingAuthorizationOptions()
	secureServingOptions            = &options.SecureServingOptions{
		BindAddress: net.ParseIP("0.0.0.0"),
		BindPort:    8443,
		Required:    true,
		ServerCert: options.GeneratableKeyCert{
			PairName:      "tf-kubernetes-configmap-backend",
			CertDirectory: "tf-kubernetes-configmap-backend/certificates",
		},
	}
)

func main() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file with authorization and master location information.")

	secureServingOptions.AddFlags(flag.CommandLine)
	delegatingAuthenticationOptions.AddFlags(flag.CommandLine)
	delegatingAuthorizationOptions.AddFlags(flag.CommandLine)

	versionFlag := flag.Bool("version", false, "Print version information and quit")

	flag.Parse()

	if *versionFlag {
		fmt.Fprintln(os.Stderr, version.Get().GitVersion)
		os.Exit(0)
	}

	authenticationClient, err := kubernetes.AuthenticationClientFromOptions(delegatingAuthenticationOptions)
	if err != nil {
		log.Fatalf("failed to create authentication client: %v", err)
	}

	authorizationClient, err := kubernetes.AuthorizationClientFromOptions(delegatingAuthorizationOptions)
	if err != nil {
		log.Fatalf("failed to create authorization client: %v", err)
	}

	coreClient, err := kubernetes.CoreClient(kubeconfig)
	if err != nil {
		log.Fatalf("failed to create core client: %v", err)
	}

	if err := secureServingOptions.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		log.Fatalf("error creating self-signed certificates: %v", err)
	}
	var secureServingInfo *server.SecureServingInfo
	if err := secureServingOptions.ApplyTo(&secureServingInfo); err != nil {
		log.Fatalf("failed to initialize secure serving options: %v", err)
	}

	internalStopCh := make(chan struct{})
	stoppedCh, err := secureServingInfo.Serve(
		tfhttp.NewHandler(coreClient, authenticationClient, authorizationClient),
		time.Duration(60)*time.Second,
		internalStopCh,
	)
	if err != nil {
		close(internalStopCh)
		log.Fatalf("failed to start serving: %v", err)
	}

	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint
		close(internalStopCh)
	}()

	<-stoppedCh
}
