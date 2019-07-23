/*
 * Copyright 2019 Mesosphere, Inc.
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
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"

	flag "github.com/spf13/pflag"
	"k8s.io/apiserver/pkg/server/options"

	tfhttp "github.com/mesosphere/tf-kubernetes-configmap-backend/pkg/http"
	"github.com/mesosphere/tf-kubernetes-configmap-backend/pkg/kubernetes"
	"github.com/mesosphere/tf-kubernetes-configmap-backend/pkg/version"
)

var (
	bindAddress net.IP
	bindPort    uint16

	kubeconfig                      string
	delegatingAuthenticationOptions = options.NewDelegatingAuthenticationOptions()
	delegatingAuthorizationOptions  = options.NewDelegatingAuthorizationOptions()
)

func main() {
	flag.IPVar(&bindAddress, "bind-address", nil, "The IP address on which to listen for the --bind-port port. If blank, all interfaces will be used (0.0.0.0 for all IPv4 interfaces and :: for all IPv6 interfaces).")
	flag.Uint16Var(&bindPort, "bind-port", 8443, "The port on which to serve HTTPS.")

	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file with authorization and master location information.")

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

	actualBindAddress := ""
	if bindAddress != nil {
		actualBindAddress = bindAddress.String()
	}

	addr := net.JoinHostPort(actualBindAddress, strconv.Itoa(int(bindPort)))
	srv := http.Server{
		Addr:    addr,
		Handler: tfhttp.NewHandler(coreClient, authenticationClient, authorizationClient),
	}
	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint

		// We received an interrupt signal, shut down.
		if err := srv.Shutdown(context.Background()); err != nil {
			// Error from closing listeners, or context timeout:
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("Listening on %s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		// Error starting or closing listener:
		log.Printf("HTTP server ListenAndServe: %v", err)
	}

	<-idleConnsClosed
}
