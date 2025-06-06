// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package envoy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	envoy_config_core_v3 "github.com/cilium/proxy/go/envoy/config/core/v3"
	envoy_extensions_tls_v3 "github.com/cilium/proxy/go/envoy/extensions/transport_sockets/tls/v3"

	"github.com/cilium/cilium/pkg/k8s/resource"
	slim_corev1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/api/core/v1"
	"github.com/cilium/cilium/pkg/logging/logfields"
)

const (
	tlsCrtAttribute = "tls.crt"
	tlsKeyAttribute = "tls.key"

	// Key for CA certificate is fixed as 'ca.crt' even though is not as "standard"
	// as 'tls.crt' and 'tls.key' are via k8s tls secret type.
	caCrtAttribute = "ca.crt"

	// Key for the TLS session ticket key is fixed as 'tls.sessionticket.key' even though is not as "standard"
	// as 'tls.crt' and 'tls.key' are via k8s tls secret type.
	//
	// The value must contain exactly 80 bytes of cryptographically-secure random data.
	//
	// In addition to the main key that is used to encrypt new sessions, up to 9 additional keys
	// can be defined. All keys are candidates for decrypting received tickets. This allows for
	// easy rotation of keys by, for example, putting the new key first, and the previous key second.
	// Additional keys are defined by using the Secret key 'tls.sessionticket.key.[1-9]'
	tlsSessionTicketKeyAttribute = "tls.sessionticket.key"

	// Key for a generic Envoy secret is fixed as 'generic' even though is not as "standard"
	// as 'tls.crt' and 'tls.key' are via k8s tls secret type.
	genericSecretAttribute = "generic"
)

// secretSyncer is responsible to sync K8s TLS Secrets in pre-defined namespaces
// via xDS SDS to Envoy.
type secretSyncer struct {
	logger         *slog.Logger
	envoyXdsServer XDSServer
}

func newSecretSyncer(logger *slog.Logger, envoyXdsServer XDSServer) *secretSyncer {
	return &secretSyncer{
		logger:         logger,
		envoyXdsServer: envoyXdsServer,
	}
}

func (r *secretSyncer) handleSecretEvent(ctx context.Context, event resource.Event[*slim_corev1.Secret]) error {
	scopedLogger := r.logger.With(
		logfields.K8sNamespace, event.Key.Namespace,
		logfields.ResourceName, event.Key.Name,
	)

	var err error

	switch event.Kind {
	case resource.Upsert:
		scopedLogger.Debug("Received Secret upsert event")
		err = r.upsertK8sSecretV1(ctx, event.Object)
		if err != nil {
			scopedLogger.Error("failed to handle Secret upsert", logfields.Error, err)
			err = fmt.Errorf("failed to handle CEC upsert: %w", err)
		}
	case resource.Delete:
		scopedLogger.Debug("Received Secret delete event")
		err = r.deleteK8sSecretV1(ctx, event.Key)
		if err != nil {
			scopedLogger.Error("failed to handle Secret delete", logfields.Error, err)
			err = fmt.Errorf("failed to handle Secret delete: %w", err)
		}
	}

	event.Done(err)

	return err
}

// updateK8sSecretV1 performs Envoy upsert operation for added or updated secret.
func (r *secretSyncer) upsertK8sSecretV1(ctx context.Context, secret *slim_corev1.Secret) error {
	if secret == nil {
		return errors.New("secret is nil")
	}

	envoySecret := r.k8sToEnvoySecret(secret)
	if envoySecret == nil {
		return nil
	}

	resource := Resources{
		Secrets: []*envoy_extensions_tls_v3.Secret{envoySecret},
	}
	return r.envoyXdsServer.UpsertEnvoyResources(ctx, resource)
}

// deleteK8sSecretV1 makes sure the related secret values in Envoy SDS is removed.
func (r *secretSyncer) deleteK8sSecretV1(ctx context.Context, key resource.Key) error {
	if len(key.Namespace) == 0 || len(key.Name) == 0 {
		return errors.New("key has empty namespace and/or name")
	}

	resource := Resources{
		Secrets: []*envoy_extensions_tls_v3.Secret{
			{
				// For deletion, only the name is required.
				Name: getEnvoySecretName(key.Namespace, key.Name),
			},
		},
	}
	return r.envoyXdsServer.DeleteEnvoyResources(ctx, resource)
}

// k8sToEnvoySecret converts k8s secret object to envoy TLS secret object
func (r *secretSyncer) k8sToEnvoySecret(secret *slim_corev1.Secret) *envoy_extensions_tls_v3.Secret {
	if secret == nil {
		return nil
	}
	envoySecret := &envoy_extensions_tls_v3.Secret{
		Name: getEnvoySecretName(secret.GetNamespace(), secret.GetName()),
	}

	switch {
	case len(secret.Data[tlsCrtAttribute]) > 0 || len(secret.Data[tlsKeyAttribute]) > 0:
		envoySecret.Type = &envoy_extensions_tls_v3.Secret_TlsCertificate{
			TlsCertificate: &envoy_extensions_tls_v3.TlsCertificate{
				CertificateChain: &envoy_config_core_v3.DataSource{
					Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
						InlineBytes: secret.Data[tlsCrtAttribute],
					},
				},
				PrivateKey: &envoy_config_core_v3.DataSource{
					Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
						InlineBytes: secret.Data[tlsKeyAttribute],
					},
				},
			},
		}

	case len(secret.Data[caCrtAttribute]) > 0:
		envoySecret.Type = &envoy_extensions_tls_v3.Secret_ValidationContext{
			ValidationContext: &envoy_extensions_tls_v3.CertificateValidationContext{
				TrustedCa: &envoy_config_core_v3.DataSource{
					Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
						InlineBytes: secret.Data[caCrtAttribute],
					},
				},
				// TODO: Consider support for other ValidationContext config.
			},
		}

	case len(secret.Data[tlsSessionTicketKeyAttribute]) > 0:
		envoySecret.Type = &envoy_extensions_tls_v3.Secret_SessionTicketKeys{
			SessionTicketKeys: &envoy_extensions_tls_v3.TlsSessionTicketKeys{
				Keys: r.getTLSSessionTicketKeys(secret.Data),
			},
		}

	case len(secret.Data[genericSecretAttribute]) > 0:
		envoySecret.Type = &envoy_extensions_tls_v3.Secret_GenericSecret{
			GenericSecret: &envoy_extensions_tls_v3.GenericSecret{
				Secret: &envoy_config_core_v3.DataSource{
					Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
						InlineBytes: secret.Data[genericSecretAttribute],
					},
				},
			},
		}
	// If we don't match any other keys, and there is only one key in the Secret,
	// then we need to drop it into a Generic Envoy secret. This is mainly for
	// handling Header secret values.
	case len(secret.Data) == 1:
		for _, data := range secret.Data {
			envoySecret.Type = &envoy_extensions_tls_v3.Secret_GenericSecret{
				GenericSecret: &envoy_extensions_tls_v3.GenericSecret{
					Secret: &envoy_config_core_v3.DataSource{
						Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
							InlineBytes: data,
						},
					},
				},
			}
		}
	default:
		return nil
	}

	return envoySecret
}

func getEnvoySecretName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func (r *secretSyncer) getTLSSessionTicketKeys(data map[string]slim_corev1.Bytes) []*envoy_config_core_v3.DataSource {
	datasourceKeys := []*envoy_config_core_v3.DataSource{}

	if len(data[tlsSessionTicketKeyAttribute]) != 80 {
		r.logger.Debug("Skipping TLS session ticket key due to not matching size of 80 chars",
			logfields.Size, len(data[tlsSessionTicketKeyAttribute]),
			logfields.Key, tlsSessionTicketKeyAttribute,
		)

		// Skipping all additional keys
		return nil
	}

	// main session encryption key
	datasourceKeys = append(datasourceKeys, &envoy_config_core_v3.DataSource{
		Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
			InlineBytes: data[tlsSessionTicketKeyAttribute],
		},
	})

	// additional keys
	for i := 1; i <= 9; i++ {
		key := fmt.Sprintf("%s.%d", tlsSessionTicketKeyAttribute, i)

		if len(data[key]) == 0 {
			continue
		}

		if len(data[key]) != 80 {
			r.logger.Debug("Skipping TLS session ticket key due to not matching size of 80 chars",
				logfields.Size, len(data[key]),
				logfields.Key, key,
			)

			// Skipping all additional keys
			continue
		}

		datasourceKeys = append(datasourceKeys, &envoy_config_core_v3.DataSource{
			Specifier: &envoy_config_core_v3.DataSource_InlineBytes{
				InlineBytes: data[key],
			},
		})
	}

	return datasourceKeys
}
