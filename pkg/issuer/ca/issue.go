package ca

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/jetstack-experimental/cert-manager/pkg/apis/certmanager/v1alpha1"
	"github.com/jetstack-experimental/cert-manager/pkg/util/kube"
	"github.com/jetstack-experimental/cert-manager/pkg/util/pki"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	errorGetCertKeyPair = "ErrGetCertKeyPair"
	errorIssueCert      = "ErrIssueCert"

	successCertIssued = "CertIssueSuccess"

	messageErrorGetCertKeyPair = "Error getting keypair for certificate: "
	messageErrorIssueCert      = "Error issuing TLS certificate: "

	messageCertIssued = "Certificate issued successfully"
)

const (
	// certificateDuration of 1 year
	certificateDuration = time.Hour * 24 * 365
	defaultOrganization = "cert-manager"
)

func (c *CA) Issue(crt *v1alpha1.Certificate) (v1alpha1.CertificateStatus, []byte, []byte, error) {
	update := crt.DeepCopy()

	signeeKey, err := kube.SecretTLSKey(c.secretsLister, c.issuer.Namespace, crt.Spec.SecretName)

	if k8sErrors.IsNotFound(err) {
		signeeKey, err = pki.GenerateRSAPrivateKey(2048)
	}

	if err != nil {
		s := messageErrorGetCertKeyPair + err.Error()
		update.UpdateStatusCondition(v1alpha1.CertificateConditionReady, v1alpha1.ConditionFalse, errorGetCertKeyPair, s)
		return update.Status, nil, nil, err
	}

	certPem, err := c.obtainCertificate(crt, &signeeKey.PublicKey)

	if err != nil {
		s := messageErrorIssueCert + err.Error()
		update.UpdateStatusCondition(v1alpha1.CertificateConditionReady, v1alpha1.ConditionFalse, errorIssueCert, s)
		return update.Status, nil, nil, err
	}

	update.UpdateStatusCondition(v1alpha1.CertificateConditionReady, v1alpha1.ConditionTrue, successCertIssued, messageCertIssued)

	return update.Status, pki.EncodePKCS1PrivateKey(signeeKey), certPem, nil
}

func (c *CA) obtainCertificate(crt *v1alpha1.Certificate, signeeKey interface{}) ([]byte, error) {
	signerCert, err := kube.SecretTLSCert(c.secretsLister, c.issuer.Namespace, c.issuer.Spec.CA.SecretRef.Name)

	if err != nil {
		return nil, fmt.Errorf("error getting issuer certificate: %s", err.Error())
	}

	signerKey, err := kube.SecretTLSKey(c.secretsLister, c.issuer.Namespace, c.issuer.Spec.CA.SecretRef.Name)

	if err != nil {
		return nil, fmt.Errorf("error getting issuer private key: %s", err.Error())
	}

	crtPem, _, err := signCertificate(crt, signerCert, signeeKey, signerKey)

	if err != nil {
		return nil, err
	}

	return crtPem, nil
}

func createCertificateTemplate(crt *v1alpha1.Certificate, publicKey interface{}) (*x509.Certificate, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %s", err.Error())
	}

	cert := &x509.Certificate{
		Version:               3,
		BasicConstraintsValid: true,
		SerialNumber:          serialNumber,
		SignatureAlgorithm:    x509.SHA256WithRSA,
		PublicKey:             publicKey,
		Subject: pkix.Name{
			Organization: []string{defaultOrganization},
			CommonName:   crt.Spec.Domains[0],
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(certificateDuration),
		// see http://golang.org/pkg/crypto/x509/#KeyUsage
		KeyUsage: x509.KeyUsageDigitalSignature,
		DNSNames: crt.Spec.Domains,
	}
	return cert, nil
}

// signCertificate returns a signed x509.Certificate object for the given
// *v1alpha1.Certificate crt.
// publicKey is the public key of the signee, and signerKey is the private
// key of the signer.
func signCertificate(crt *v1alpha1.Certificate, issuerCert *x509.Certificate, publicKey interface{}, signerKey interface{}) ([]byte, *x509.Certificate, error) {
	template, err := createCertificateTemplate(crt, publicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating x509 certificate template: %s", err.Error())
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, issuerCert, publicKey, signerKey)

	if err != nil {
		return nil, nil, fmt.Errorf("error creating x509 certificate: %s", err.Error())
	}

	cert, err := pki.DecodeDERCertificateBytes(derBytes)

	if err != nil {
		return nil, nil, fmt.Errorf("error decoding DER certificate bytes: %s", err.Error())
	}

	pemBytes := bytes.NewBuffer([]byte{})
	err = pem.Encode(pemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return nil, nil, fmt.Errorf("error encoding certificate PEM: %s", err.Error())
	}
	return pemBytes.Bytes(), cert, err
}
