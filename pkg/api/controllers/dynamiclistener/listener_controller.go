// This is one of the api-controllers which have a responsibility to decide protocol to listen and certificates
package dynamiclistener

import (
	"context"
	"net/http"

	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/cert"
	"github.com/rancher/rancher/pkg/dynamiclistener"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Controller struct {
	listenConfig       v3.ListenConfigInterface
	listenConfigLister v3.ListenConfigLister
	secrets            v1.SecretInterface
	server             *dynamiclistener.Server
}

type storageUpdater interface {
	Update(*v3.ListenConfig) (*v3.ListenConfig, error)
}

type storage struct {
	storageUpdater
	v3.ListenConfigLister
}

func Start(ctx context.Context, context *config.ScaledContext, httpPort, httpsPort int, handler http.Handler) {
	s := storage{
		storageUpdater:     context.Management.ListenConfigs(""),
		ListenConfigLister: context.Management.ListenConfigs("").Controller().Lister(),
	}
	c := &Controller{
		server:             dynamiclistener.NewServer(ctx, s, handler, httpPort, httpsPort),
		secrets:            context.Core.Secrets(""),
		listenConfig:       context.Management.ListenConfigs(""),
		listenConfigLister: context.Management.ListenConfigs("").Controller().Lister(),
	}
	context.Management.ListenConfigs("").AddHandler("listener", c.sync)
	go func() {
		<-ctx.Done()
		c.server.Shutdown()
	}()
}

func (c *Controller) sync(key string, listener *v3.ListenConfig) error {
	if listener == nil {
		return nil
	}

	if listener.Enabled {
		return c.enable(listener)
	}

	c.server.Disable(listener)
	allConfigs, err := c.listenConfigLister.List("", labels.Everything())
	if err != nil {
		return err
	}

	var lastConfig *v3.ListenConfig
	for _, config := range allConfigs {
		if !config.Enabled || config.DeletionTimestamp != nil {
			continue
		}
		if lastConfig == nil || lastConfig.CreationTimestamp.Before(&config.CreationTimestamp) {
			lastConfig = config
		}
	}

	if lastConfig != nil {
		return c.enable(listener)
	}

	return nil
}

func (c *Controller) enable(listener *v3.ListenConfig) error {
	current, err := c.server.Enable(listener)
	if err != nil {
		return err
	}
	if current {
		return c.updateCurrent(listener)
	}
	return nil
}

func (c *Controller) updateCurrent(listener *v3.ListenConfig) error {
	settings.CACerts.Set(listener.CACerts)
	if listener.Key != "" && listener.CACerts != "" && listener.Cert != "" {
		certInfo, err := cert.Info(listener.Cert+"\n"+listener.CACerts, listener.Key)
		if err != nil {
			return err
		}

		if certInfo.SerialNumber != listener.SerialNumber {
			copy := listener.DeepCopy()
			copy.CertFingerprint = certInfo.Fingerprint
			copy.CN = certInfo.CN
			copy.Version = certInfo.Version
			copy.ExpiresAt = convert.ToString(certInfo.ExpiresAt)
			copy.Issuer = certInfo.Issuer
			copy.IssuedAt = convert.ToString(certInfo.IssuedAt)
			copy.Algorithm = certInfo.Algorithm
			copy.SerialNumber = certInfo.SerialNumber
			copy.KeySize = certInfo.KeySize
			copy.SubjectAlternativeNames = certInfo.SubjectAlternativeNames
			_, err := c.listenConfig.Update(copy)
			if err != nil {
				return err
			}
		}
	}

	return c.saveCACertToSecret(listener.CAKey, listener.CACert)
}

func (c *Controller) saveCACertToSecret(key, cert string) error {
	if key == "" || cert == "" {
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tls-rancher",
			Namespace: "cattle-system",
		},
		StringData: map[string]string{
			"tls.key": key,
			"tls.crt": cert,
		},
		Type: corev1.SecretTypeTLS,
	}

	existing, err := c.secrets.GetNamespaced(secret.Namespace, secret.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		_, err = c.secrets.Create(secret)
		return err
	} else if err != nil {
		return err
	}

	existing.StringData = secret.StringData
	_, err = c.secrets.Update(existing)
	return err
}
