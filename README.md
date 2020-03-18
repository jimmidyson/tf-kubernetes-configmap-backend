# Terraform Kubernetes ConfigMap Backend

![Docker Image Version (latest semver)](https://img.shields.io/docker/v/jimmidyson/tf-kubernetes-configmap-backend?sort=semver&style=for-the-badge)
![Docker Image Size (latest semver)](https://img.shields.io/docker/image-size/jimmidyson/tf-kubernetes-configmap-backend?sort=semver&style=for-the-badge)

This project provides a service that can be used as a [Terraform](https://www.terraform.io/) [http backend](https://www.terraform.io/docs/backends/types/http.html) to store Terraform state in [Kubernetes](https://kubernetes.io/) [ConfigMaps](https://kubernetes.io/docs/tasks/configure-pod-container/configure-pod-configmap/#create-a-configmap), with [optional state locking](#optional-state-locking).

## Terraform backend configuration

The web server serves all routes of the form `/<target_configmap_namespace>/<target_configmap_name>`. The path specifies the `configmap` that the Terraform requests manage. This path should be specified in `address` to configure Terraform to use that path for retrieving and storing state.

Optionally, the same path can be used for `lock_address` and `unlock_address`, which will configure Terraform to perform state locking (i.e. send `LOCK` and `UNLOCK` requests) for all operations that could write state. See [optional state locking](#optional-state-locking) for more details.

```hcl
terraform {
  backend "http" {
    address        = "https://<service_address>/<destination_configmap_namespace>/<destination_configmap_name>"
    lock_address   = "https://<service_address>/<destination_configmap_namespace>/<destination_configmap_name>" # Optional
    unlock_address = "https://<service_address>/<destination_configmap_namespace>/<destination_configmap_name>" # Optional

    username       = "terraform" # Username is not required for authentication but must be specified for Terraform to send required basic authentication headers
    password       = "<token>" # See Authentication for details of how this token is authenticates
  }
}
```

If using Kubernetes to run your provisioning jobs, you can use `tf-kubernetes-configmap-backend-file-generator` as an `initContainer` to generate this file at runtime. This populates the Terraform backend config, using the pod's service account token for the value of the `password` field.

## Authentication

Terraform only supports sending [basic authentication](https://en.wikipedia.org/wiki/Basic_access_authentication) headers to authenticate to an `http` backend, hence `tf-kubernetes-configmap-backend` is secured with basic authentication. The username is currently ignored. The `password` part of the basic authentication header is expected to be a valid Kubernetes token. This token is validated by making a `authentication.k8s.io/v1beta1.TokenReview` request to the Kubernetes API server, similar to how webhook authentication works for the Kubernetes API server. The response from the API server indicates whether the token is valid (authenticated) and the user ID of the requester. This user ID is used to [authorize the request](#authorization).

## Authorization

Once the requster is authenticated and the user ID is retrieved, `tf-kubernetes-configmap-backend` makes a `authorization.k8s.io/v1.SubjectAccessReview` request to check if the requester is authorized to `create`, `update`, or `delete` the specified `configmap` as required (different Terraform operations have different requirements - this is all transparently handled by `tf-kubernetes-configmap-backend`).

## Optional state locking

`tf-kubernetes-configmap-backend` supports state locking if Terraform sends the `LOCK` and `UNLOCK` requests, enabled by configuring `lock_address` and `unlock_address`. Terraform requests state locking by sending a `LOCK` request (an HTTP request with verb of `LOCK`). The request contains lock information, most importantly a lock ID, which is a generated UUID: a unique identifier for every single operation.

`tf-kubernetes-configmap-backend` uses annotations on the targeted `configmap` to perform state locking. On receiving a lock request, `tf-kubernetes-configmap-backend` compares the lock ID in the body of the request with the current value of the `tf-kubernetes-configmap-backend.jimmidyson.github.com/lock-id` annotation. If the annotation is not present or matches the current value, then the `configmap` annotations are updated to indicate that it is locked. If the annotation is present and the value does not match the current value, then `tf-kubernetes-configmap-backend` returns a `423 Locked` with the current lock info, following the behaviour defined in the [Terraform docs](https://www.terraform.io/docs/backends/types/http.html).

On receiving `UNLOCK`, the same behaviour applies and is only unlocked if the requester lock ID matches the current lock ID in the `configmap` annotations.

Following standard Terraform behaviour, to forcibly unlock state (e.g. in the case of a zombie process holding the lock), either run `terraform force-unlock <lock_id> -force` or remove the annotations prefixed with `tf-kubernetes-configmap-backend.jimmidyson.github.com/` directly from the `configmap`. This will allow future processes to lock the state again.

## State compression and minification

Kubernetes `configmap` have a maximum size of 1MB, which is sufficient for small Terraform states, but is not sufficient for medium/large Terraform states. Terraform state is stored in JSON format and as such can be both minified (removal of redundant whitespace) and compressed (`tf-kubernetes-configmap-backend` uses GZIP compression). This allows for even very large state files to be stored in the `configmap`. In basic benchmarking, this allowed a 300MB state file to be compressed to a size small enough to fit in the `configmap`.

## Usage

Most flags come from the Kubernetes ecosystem to provide secure serving, authentication and authorization configuration. It looks like a lot of flags, but general usage can be simplified to:

```shell
$ tf-kubernetes-configmap-backend \
  --authentication-kubeconfig <authentication_kubeconfig> \
  --authorization-kubeconfig <authorization_kubeconfig> \
  --kubeconfig <core_kubeconfig> \
  --compress-state --minify
```

Further customization via flags is possible. The full list of flags:

```shell
$ tf-kubernetes-configmap-backend --help
Usage of tf-kubernetes-configmap-backend:
      --authentication-kubeconfig string                        kubeconfig file pointing at the 'core' kubernetes server with enough rights to create tokenaccessreviews.authentication.k8s.io.
      --authentication-skip-lookup                              If false, the authentication-kubeconfig will be used to lookup missing authentication configuration from the cluster.
      --authentication-token-webhook-cache-ttl duration         The duration to cache responses from the webhook token authenticator. (default 10s)
      --authentication-tolerate-lookup-failure                  If true, failures to look up missing authentication configuration from the cluster are not considered fatal. Note that this can result in authentication that treats all requests as anonymous.
      --authorization-always-allow-paths strings                A list of HTTP paths to skip during authorization, i.e. these are authorized without contacting the 'core' kubernetes server.
      --authorization-kubeconfig string                         kubeconfig file pointing at the 'core' kubernetes server with enough rights to create subjectaccessreviews.authorization.k8s.io.
      --authorization-webhook-cache-authorized-ttl duration     The duration to cache 'authorized' responses from the webhook authorizer. (default 10s)
      --authorization-webhook-cache-unauthorized-ttl duration   The duration to cache 'unauthorized' responses from the webhook authorizer. (default 10s)
      --bind-address ip                                         The IP address on which to listen for the --secure-port port. The associated interface(s) must be reachable by the rest of the cluster, and by CLI/web clients. If blank, all interfaces will be used (0.0.0.0 for all IPv4 interfaces and :: for all IPv6 interfaces). (default 0.0.0.0)
      --cert-dir string                                         The directory where the TLS certs are located. If --tls-cert-file and --tls-private-key-file are provided, this flag will be ignored. (default "tf-kubernetes-configmap-backend/certificates")
      --client-ca-file string                                   If set, any request presenting a client certificate signed by one of the authorities in the client-ca-file is authenticated with an identity corresponding to the CommonName of the client certificate.
      --compress-state                                          Enable compression of the stored Terraform state
      --http2-max-streams-per-connection int                    The limit that the server gives to clients for the maximum number of streams in an HTTP/2 connection. Zero means to use golang's default.
      --kubeconfig string                                       Path to kubeconfig file with authorization and master location information.
      --log-flush-frequency duration                            Maximum number of seconds between log flushes (default 5s)
      --minify-state                                            Enable minification of stored Terraform state
      --requestheader-allowed-names strings                     List of client certificate common names to allow to provide usernames in headers specified by --requestheader-username-headers. If empty, any client certificate validated by the authorities in --requestheader-client-ca-file is allowed.
      --requestheader-client-ca-file string                     Root certificate bundle to use to verify client certificates on incoming requests before trusting usernames in headers specified by --requestheader-username-headers. WARNING: generally do not depend on authorization being already done for incoming requests.
      --requestheader-extra-headers-prefix strings              List of request header prefixes to inspect. X-Remote-Extra- is suggested. (default [x-remote-extra-])
      --requestheader-group-headers strings                     List of request headers to inspect for groups. X-Remote-Group is suggested. (default [x-remote-group])
      --requestheader-username-headers strings                  List of request headers to inspect for usernames. X-Remote-User is common. (default [x-remote-user])
      --secure-port int                                         The port on which to serve HTTPS with authentication and authorization.It cannot be switched off with 0. (default 8443)
      --tls-cert-file string                                    File containing the default x509 Certificate for HTTPS. (CA cert, if any, concatenated after server cert). If HTTPS serving is enabled, and --tls-cert-file and --tls-private-key-file are not provided, a self-signed certificate and key are generated for the public address and saved to the directory specified by --cert-dir.
      --tls-cipher-suites strings                               Comma-separated list of cipher suites for the server. If omitted, the default Go cipher suites will be use.  Possible values: TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,TLS_ECDHE_ECDSA_WITH_RC4_128_SHA,TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,TLS_ECDHE_RSA_WITH_RC4_128_SHA,TLS_RSA_WITH_3DES_EDE_CBC_SHA,TLS_RSA_WITH_AES_128_CBC_SHA,TLS_RSA_WITH_AES_128_CBC_SHA256,TLS_RSA_WITH_AES_128_GCM_SHA256,TLS_RSA_WITH_AES_256_CBC_SHA,TLS_RSA_WITH_AES_256_GCM_SHA384,TLS_RSA_WITH_RC4_128_SHA
      --tls-min-version string                                  Minimum TLS version supported. Possible values: VersionTLS10, VersionTLS11, VersionTLS12, VersionTLS13
      --tls-private-key-file string                             File containing the default x509 private key matching --tls-cert-file.
      --tls-sni-cert-key namedCertKey                           A pair of x509 certificate and private key file paths, optionally suffixed with a list of domain patterns which are fully qualified domain names, possibly with prefixed wildcard segments. If no domain patterns are provided, the names of the certificate are extracted. Non-wildcard matches trump over wildcard matches, explicit domain patterns trump over extracted names. For multiple key/certificate pairs, use the --tls-sni-cert-key multiple times. Examples: "example.crt,example.key" or "foo.crt,foo.key:*.foo.com,foo.com". (default [])
      --version                                                 Print version information and quit
```
