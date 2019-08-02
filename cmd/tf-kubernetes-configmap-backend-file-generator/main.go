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
	"io/ioutil"
	"net/url"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
)

var (
	outputFilePath string
	remoteAddress  = &url.URL{}
)

type urlValue url.URL

func newURLValue(p *url.URL) *urlValue {
	return (*urlValue)(p)
}

func (u *urlValue) String() string {
	return (*url.URL)(u).String()
}

func (u *urlValue) Set(s string) error {
	parsed, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("failed to parse URL: %q", s)
	}
	*u = urlValue(*parsed)
	return nil
}

func (u *urlValue) Type() string {
	return "url"
}

var _ flag.Value = newURLValue(remoteAddress)

func main() {
	flag.StringVar(&outputFilePath, "output-file", "", "Path to generated output file.")
	flag.Var(newURLValue(remoteAddress), "http-backend-address", "The address of the Terraform backend REST endpoint.")

	flag.Parse()

	serviceAccountToken, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		fmt.Printf("Failed to read service account token: %v\n", err)
		os.Exit(1)
	}

	generatedContents := `address = "` + remoteAddress.String() + `"
username = "terraform" # Value is unused, only password (token) is used for authentication
password = "` + strings.TrimSpace(string(serviceAccountToken)) + `"
skip_cert_verification = "true"
`

	if err := ioutil.WriteFile(outputFilePath, []byte(generatedContents), os.FileMode(0444)); err != nil {
		fmt.Printf("Failed to write generated file: %v\n", err)
		os.Exit(1)
	}
}
